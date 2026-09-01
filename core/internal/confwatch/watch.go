// Package confwatch, konferans odalarını arka planda izler: bir katılımcı
// ayrıldığında odada TEK kişi kalıp kalmadığına bakar, kaldıysa o kişiye
// "odada yalnız kaldınız" anonsu çalar ve kısa bir süre sonra onu da
// çıkarıp odayı kapatır.
//
// Neden gerekli: Asterisk ConfBridge'in kendi başına "son kişi kalınca
// odayı kapat" diye bir özelliği yok (sadece announce_only_user ile
// anons çalınabiliyor, kapatmıyor) — bu davranışı biz AMI event'lerini
// dinleyerek uyguluyoruz (Konferans — Kapatma Ekranı ve Kısayol
// Eksiklikleri notu, 2026-09-01 devamı).
package confwatch

import (
	"log"
	"time"

	"rnvcs-yonetim-servisi/internal/ami"
)

// announceFile, Asterisk'in kendi çekirdek ses paketinde (core-sounds)
// hazır gelen "conf-onlyone" dosyasıdır ("You are currently the only
// person in this conference"). Özel bir kayıt gerekmez.
const announceFile = "conf-onlyone"

// graceAfterAnnounce, anonsu çaldıktan sonra kişiyi odadan çıkarmadan
// önce beklenen süre (anonsun bitmesi için).
const graceAfterAnnounce = 3 * time.Second

// reconnectDelay, AMI bağlantısı koparsa yeniden denemeden önce beklenen süre.
const reconnectDelay = 5 * time.Second

// Start, arka planda sonsuz bir izleme döngüsü başlatır (bloklamaz —
// kendi goroutine'inde çalışır). amiAddr/amiUser boşsa (konferans özelliği
// bu sunucuda yapılandırılmamışsa) hiçbir şey yapmadan çıkar.
func Start(amiAddr, amiUser, amiSecret string) {
	if amiAddr == "" || amiUser == "" {
		return
	}
	go func() {
		for {
			if err := watchOnce(amiAddr, amiUser, amiSecret); err != nil {
				log.Printf("confwatch: AMI event dinleme koptu, %s sonra yeniden denenecek: %v", reconnectDelay, err)
			}
			time.Sleep(reconnectDelay)
		}
	}()
}

func watchOnce(amiAddr, amiUser, amiSecret string) error {
	c, err := ami.Dial(amiAddr, amiUser, amiSecret)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.EnableEvents(); err != nil {
		return err
	}
	return c.ListenEvents(func(pkt map[string]string) {
		switch pkt["Event"] {
		case "ConfbridgeLeave", "ConfbridgeJoin":
			room := pkt["Conference"]
			if room == "" {
				return
			}
			go checkLoneParticipant(amiAddr, amiUser, amiSecret, room)
		}
	})
}

// checkLoneParticipant, roomCode odasını AMI'den yeniden sorgular (event
// başlıklarındaki üye sayısına güvenmek yerine — Asterisk sürümüne göre
// bu alan değişebiliyor) ve oda TAM OLARAK 1 kişi içeriyorsa o kişiye
// anons çalıp çıkarır.
func checkLoneParticipant(amiAddr, amiUser, amiSecret, room string) {
	// Katılımcı ayrılırken/katılırken kanal durumu bir an için tutarsız
	// olabilir — kısa bir bekleme ile durumun oturmasını bekliyoruz.
	time.Sleep(800 * time.Millisecond)

	c, err := ami.Dial(amiAddr, amiUser, amiSecret)
	if err != nil {
		log.Printf("confwatch: %s odası kontrol edilemedi (AMI bağlanamadı): %v", room, err)
		return
	}
	defer c.Close()

	members, err := c.ConfbridgeList(room)
	if err != nil {
		log.Printf("confwatch: %s odası üyeleri okunamadı: %v", room, err)
		return
	}
	if len(members) != 1 {
		return // oda boş, kapanmış ya da hâlâ 2+ kişi var — bir şey yapma
	}
	last := members[0]

	// Anonsu çal (desteklenmiyorsa/başarısız olursa yine de devam et —
	// kişiyi tek başına odada bırakmamak, anonstan daha öncelikli).
	if err := c.ConfbridgePlayFile(room, last.Channel, announceFile); err != nil {
		log.Printf("confwatch: %s odasında %s anons çalınamadı (yine de kapatılacak): %v", room, last.Channel, err)
	} else {
		time.Sleep(graceAfterAnnounce)
	}

	// Anons sırasında biri odaya katılmış olabilir — son kez doğrula.
	members, err = c.ConfbridgeList(room)
	if err != nil || len(members) != 1 {
		return
	}
	if err := c.ConfbridgeKick(room, last.Channel); err != nil {
		log.Printf("confwatch: %s odasında %s çıkarılamadı: %v", room, last.Channel, err)
	}
}
