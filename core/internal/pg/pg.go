// Package pg, PostgreSQL'e "psql" komut satırı istemcisi üzerinden erişir.
//
// NEDEN bir Go PostgreSQL sürücüsü (örn. lib/pq) DEĞİL: bu proje boyunca
// (audiowatch paketinde de olduğu gibi) Go kodu SIFIR harici bağımlılıkla
// yazıldı, çünkü CORE/Panel gibi üretim makinelerinin genel internet
// erişimi olsa da (apt çalışıyor), Go modül proxy'sine (proxy.golang.org)
// erişimin her ortamda garanti olmadığı görüldü. `psql` zaten
// core_kurulum.sh ile kurulu ve PostgreSQL'in resmi istemcisi olduğu
// için, ekstra bir "go mod tidy" / internet bağımlılığı yaratmadan
// güvenilir bir yol sağlıyor.
package pg

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// DB, bir PostgreSQL bağlantı string'ini (libpq URI formatı,
// örn. "postgresql://rnvcs:sifre@localhost:5432/rnvcs?sslmode=disable")
// sarmalar.
type DB struct {
	ConnStr string
}

func Open(connStr string) *DB {
	return &DB{ConnStr: connStr}
}

// Ping, bağlantının çalıştığını doğrular.
func (d *DB) Ping() error {
	_, err := d.Query("SELECT 1")
	return err
}

// Query, SQL'i çalıştırır ve satırları (sekme ile ayrılmış alanlar
// halinde) döner. Sonuç yoksa nil, nil döner.
func (d *DB) Query(sql string) ([][]string, error) {
	cmd := exec.Command("psql", d.ConnStr, "-v", "ON_ERROR_STOP=1", "-tA", "-F", "\t", "-c", sql)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("psql hatası: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	text := strings.TrimRight(out.String(), "\n")
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	rows := make([][]string, len(lines))
	for i, line := range lines {
		rows[i] = strings.Split(line, "\t")
	}
	return rows, nil
}

// QueryRow, tek satır bekler; satır yoksa hata döner.
func (d *DB) QueryRow(sql string) ([]string, error) {
	rows, err := d.Query(sql)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("satır bulunamadı")
	}
	return rows[0], nil
}

// Exec, sonuç beklemeyen SQL komutlarını (INSERT/UPDATE/DDL) çalıştırır.
func (d *DB) Exec(sql string) error {
	_, err := d.Query(sql)
	return err
}

// EscapeLiteral, bir string'i SQL literal olarak güvenli hale getirir
// (tek tırnakları ikiye katlar). Sorgularda TÜM kullanıcı girdisi
// (username, panel_code vb.) bu fonksiyondan geçirilmeden asla SQL
// string'ine gömülmemeli.
func EscapeLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// Bool, bir Go bool'unu Postgres literal'ine çevirir.
func Bool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
