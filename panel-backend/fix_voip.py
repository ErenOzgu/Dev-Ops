#!/usr/bin/env python3
import re, sys

def find_func_body_brace(text, idx):
    # "chan struct{}" gibi BOŞ süslü parantez çiftlerini atla — asıl fonksiyon
    # gövdesinin açılış parantezi bu değil, ondan SONRA gelen ilk gerçek "{"
    i = idx
    while True:
        i = text.find('{', i)
        if i == -1:
            return -1
        if text[i:i+2] == '{}':
            i += 2
            continue
        return i

def replace_func(text, signature_start, new_body):
    idx = text.find(signature_start)
    if idx == -1:
        print("UYARI: bulunamadi ->", signature_start[:60])
        sys.exit(1)
    brace_start = find_func_body_brace(text, idx)
    if brace_start == -1:
        print("UYARI: fonksiyon govdesi bulunamadi ->", signature_start[:60])
        sys.exit(1)
    depth = 0
    i = brace_start
    while i < len(text):
        if text[i] == '{':
            depth += 1
        elif text[i] == '}':
            depth -= 1
            if depth == 0:
                break
        i += 1
    end = i + 1
    return text[:idx] + new_body + text[end:]

# ---------------- engine.go ----------------
path = "internal/voip/engine.go"
with open(path) as f:
    t = f.read()

t = t.replace(
    "respCh  chan string",
    "respCh  chan string\n\trespWaiters map[string]chan string",
    1,
)

t = t.replace(
    "e.respCh = make(chan string, 4)",
    "e.respCh = make(chan string, 4)\n\te.respWaiters = make(map[string]chan string)",
    1,
)

new_send_and_await = '''func (e *Engine) sendAndAwait(conn *net.UDPConn, remote *net.UDPAddr, msg, callID string) (string, error) {
	ch := e.registerWaiter(callID)
	defer e.unregisterWaiter(callID)
	if _, err := conn.WriteToUDP([]byte(msg), remote); err != nil {
		return "", fmt.Errorf("istek gonderilemedi: %w", err)
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(5 * time.Second):
		return "", fmt.Errorf("yanit zaman asimi (Asterisk erisilebilir mi, SIP/UDP portu acik mi?)")
	}
}'''
t = replace_func(t, "func (e *Engine) sendAndAwait(", new_send_and_await)

t = t.replace(
    "resp1, err := e.sendAndAwait(conn, remoteAddr, msg1)",
    "resp1, err := e.sendAndAwait(conn, remoteAddr, msg1, callID)",
    1,
)
t = t.replace(
    "resp2, err := e.sendAndAwait(conn, remoteAddr, msg2)",
    "resp2, err := e.sendAndAwait(conn, remoteAddr, msg2, callID)",
    1,
)

new_read_loop = '''func (e *Engine) readLoop(conn *net.UDPConn) {
	buf := make([]byte, 8192)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return // soket kapatildi
		}
		msg := string(buf[:n])
		if strings.HasPrefix(msg, "SIP/2.0") {
			// DUZELTME (2026-08-25): cevaplar artik paylasilan TEK bir kanala
			// degil, Call-ID'sine gore dogru bekleyen istege yonlendiriliyor.
			// Oncesinde REGISTER yenilemesi ile giden bir INVITE ayni kanaldan
			// okudugu icin birbirlerinin (or. eski nonce iceren) cevabini
			// calabiliyordu.
			cid := extractCallID(msg)
			e.mu.Lock()
			ch := e.respWaiters[cid]
			e.mu.Unlock()
			if ch != nil {
				select {
				case ch <- msg:
				default:
				}
			}
			continue
		}
		e.handleRequest(msg, from)
	}
}

var callIDRe = regexp.MustCompile(`(?im)^Call-ID:\\s*(.+?)\\s*$`)

func extractCallID(msg string) string {
	m := callIDRe.FindStringSubmatch(msg)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func (e *Engine) registerWaiter(callID string) chan string {
	ch := make(chan string, 4)
	e.mu.Lock()
	if e.respWaiters == nil {
		e.respWaiters = make(map[string]chan string)
	}
	e.respWaiters[callID] = ch
	e.mu.Unlock()
	return ch
}

func (e *Engine) unregisterWaiter(callID string) {
	e.mu.Lock()
	delete(e.respWaiters, callID)
	e.mu.Unlock()
}'''
t = replace_func(t, "func (e *Engine) readLoop(", new_read_loop)

new_refresh_loop = '''func (e *Engine) refreshLoop(cfg Config, stopCh chan struct{}) {
	interval := time.Duration(float64(cfg.ExpiresSec)*0.7) * time.Second
	if interval < 15*time.Second {
		interval = 15 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-t.C:
			err := e.register(cfg.ExpiresSec)
			e.mu.Lock()
			if err != nil {
				e.regLastError = err.Error()
				e.registered = false
			} else {
				e.regLastError = ""
				e.registered = true
				e.regLastOK = time.Now()
			}
			e.mu.Unlock()
			if err != nil {
				log.Printf("!! REGISTER yenileme basarisiz (bir sonraki turda tekrar denenecek): %v", err)
				// DUZELTME (2026-08-25): oncesinde burada "return" ile dongu
				// KALICI olarak duruyordu - respWaiters duzeltmesinden onceki
				// tek seferlik bir catisma bile kaydi sonsuza kadar dusuruyordu.
				// Artik hata olsa da bir sonraki ticker turunda tekrar
				// denenmeye devam ediliyor.
			}
		}
	}
}'''
t = replace_func(t, "func (e *Engine) refreshLoop(", new_refresh_loop)

with open(path, "w") as f:
    f.write(t)
print("engine.go guncellendi.")

# ---------------- outbound.go ----------------
path2 = "internal/voip/outbound.go"
with open(path2) as f:
    t2 = f.read()

new_invite_transaction = '''func (e *Engine) inviteTransaction(conn *net.UDPConn, remote *net.UDPAddr, msg, callID string) (string, error) {
	ch := e.registerWaiter(callID)
	defer e.unregisterWaiter(callID)
	if _, err := conn.WriteToUDP([]byte(msg), remote); err != nil {
		return "", fmt.Errorf("INVITE gonderilemedi: %w", err)
	}
	const ringTimeout = 30 * time.Second
	deadline := time.Now().Add(ringTimeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return "", fmt.Errorf("cagri zaman asimina ugradi (yanit yok)")
		}
		select {
		case resp := <-ch:
			code, _ := statusCode(resp)
			if code >= 200 {
				return resp, nil
			}
			deadline = time.Now().Add(ringTimeout)
		case <-time.After(remaining):
			return "", fmt.Errorf("cagri zaman asimina ugradi (yanit yok)")
		}
	}
}'''
t2 = replace_func(t2, "func (e *Engine) inviteTransaction(", new_invite_transaction)

t2 = t2.replace(
    "finalResp, err := e.inviteTransaction(conn, remoteAddr, msg)",
    "finalResp, err := e.inviteTransaction(conn, remoteAddr, msg, callID)",
    1,
)
t2 = t2.replace(
    "retryResp, err := e.inviteTransaction(conn, remoteAddr, retryMsg)",
    "retryResp, err := e.inviteTransaction(conn, remoteAddr, retryMsg, callID)",
    1,
)

with open(path2, "w") as f:
    f.write(t2)
print("outbound.go guncellendi.")
