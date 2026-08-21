# RNVCS — Tam Otomatik CI/CD Tasarımı

**Amaç:** Koddan (push) → teste → pakete → dağıtıma kadar olan zinciri, insan müdahalesini yalnızca "üretime al" kararına indirgeyecek şekilde otomatikleştirmek.

Bu belge dört dosyayla birlikte gelir:
- `.gitea/workflows/ci.yml` — her push/PR'da lint+test+migration testi
- `.gitea/workflows/release.yml` — sürüm etiketi (`vX.Y.Z`) atılınca otomatik derle+paketle+**yedek CORE'a** dağıt
- `.gitea/workflows/deploy-production.yml` — **elle tetiklenen**, üretime dağıtım (otomatik rollback'li)
- `scripts/deploy_apply.sh`, `scripts/rollback.sh`, `scripts/backup_current.sh` — hedef sunucularda çalışan uygulama betikleri

---

## 1. Otomasyon Seviyesi — Neresi Otomatik, Neresi Değil (bilinçli tasarım)

```
push (herhangi bir dal)          → CI (lint+test+migration testi)         → TAM OTOMATİK
main'e merge                     → (aynı CI tekrar)                       → TAM OTOMATİK
git tag vX.Y.Z && git push --tags → derle + paketle + YEDEK CORE'a dağıt   → TAM OTOMATİK
                                     + yedek CORE'da smoke test            → TAM OTOMATİK
                                     (smoke test FAIL ederse burada durur, → OTOMATİK DURDURMA
                                      üretime hiç gitmez)
Gitea arayüzünden "Run Workflow" → ÜRETİME dağıt + smoke test             → SADECE ELLE TETİKLENİR
                                     (smoke test FAIL ederse otomatik      → OTOMATİK ROLLBACK
                                      rollback tetiklenir)
```

**Neden üretim adımı tam otomatik değil?** Çünkü geri dönüşü en pahalı olan adım o — bir push'un otomatik olarak dakikalar içinde gerçek trafiğe çıkmasını istemeyiz. Yedek CORE'a kadar her şey otomatik (o zaten trafik almıyor, düşük risk); üretime geçiş bilinçli, tek tıklık bir insan kararı. Bu, yol haritasındaki "rollback stratejisi" ve "canary" prensipleriyle birebir uyumlu.

---

## 2. Kurulum Adımları (rnvcs-ci01 üzerinde)

### 2.1 Gitea Actions runner'ı kaydetme

Gitea Actions, ayrı bir `act_runner` binary'si ister — Gitea'nın kendisi sadece iş kuyruğunu tutar, işi bu runner çalıştırır.

```bash
mkdir -p /opt/rnvcs-ci/runner && cd /opt/rnvcs-ci/runner
curl -L https://gitea.com/gitea/act_runner/releases/latest/download/act_runner-linux-amd64 -o act_runner
chmod +x act_runner
```

Gitea arayüzünden: **Site Yönetimi → Actions → Runners → Create new Runner** (ya da repo bazlı istiyorsanız repo → Settings → Actions → Runners). Karşınıza bir **registration token** çıkacak — kopyalayın.

```bash
./act_runner register --instance http://<GITEA_IP>:3000 --token <KAYIT_TOKENI> --no-interactive
```

Runner'ı **Docker executor** ile çalıştırın (önemli — `ci.yml`'deki Postgres "services" bloğu bunu gerektirir):

```bash
cat > /opt/rnvcs-ci/runner/config.yaml <<'EOF'
container:
  network: bridge
EOF

# systemd servisi olarak kalıcı hale getirin:
sudo tee /etc/systemd/system/act-runner.service > /dev/null <<'EOF'
[Unit]
Description=Gitea Actions Runner
After=docker.service
Requires=docker.service

[Service]
WorkingDirectory=/opt/rnvcs-ci/runner
ExecStart=/opt/rnvcs-ci/runner/act_runner daemon --config /opt/rnvcs-ci/runner/config.yaml
User=rn
Restart=always

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now act-runner
sudo systemctl status act-runner
```

Gitea arayüzünde runner'ın "Idle/Online" göründüğünü doğrulayın.

### 2.2 Repo'ya workflow dosyalarını ekleme

Bu paketteki `.gitea/workflows/` klasörünü, repo kökünüzdeki `.gitea/workflows/` ile birleştirin (repoda yoksa direkt kopyalayın):

```bash
cd ~/work/rnvcs
mkdir -p .gitea/workflows
# (bu paketteki üç .yml dosyasını buraya kopyalayın)
# scripts/deploy_apply.sh, rollback.sh, backup_current.sh de scripts/ altına
chmod +x scripts/deploy_apply.sh scripts/rollback.sh scripts/backup_current.sh
git add .gitea scripts
git commit -m "CI/CD: Gitea Actions pipeline (ci + release + deploy-production)"
git push origin main
```

Push eder etmez `ci.yml` otomatik tetiklenip çalışmaya başlayacak — Gitea'da **İşlemler** (Actions) sekmesinden izleyebilirsiniz.

### 2.3 Secrets tanımlama (Gitea → repo → Settings → Actions → Secrets)

| Secret adı | Değer | Açıklama |
|---|---|---|
| `DEPLOY_SSH_KEY` | Deploy için üretilecek **özel** SSH private key'in tam içeriği | Aşağıda 2.4'te üretimi anlatılıyor |
| `DEPLOY_USER` | ör. `rnvcs-deploy` | Hedef sunuculardaki kısıtlı deploy kullanıcısı |
| `YEDEK_CORE_HOST` | `15.2.4.11` | |
| `PROD_CORE_HOST` | `15.2.4.10` | |
| `PANEL_HOST` | `15.2.4.201` | |

### 2.4 Hedef sunucularda deploy kullanıcısı hazırlama

**Genel kural: CI'nin deploy anahtarı, sizin kişisel SSH anahtarınızdan AYRI ve kısıtlı olmalı.** Her hedef sunucuda (yedek CORE, üretim CORE, panel):

```bash
# Hedef sunucuda (ör. 15.2.4.11'de):
sudo useradd -m -s /bin/bash rnvcs-deploy
sudo mkdir -p /opt/rnvcs/{incoming,releases,db-backups,scripts}
sudo chown -R rnvcs-deploy:rnvcs-deploy /opt/rnvcs
```

rnvcs-ci01'de deploy anahtarını üretin (bir kere, tüm hedefler için ortak kullanılabilir):

```bash
ssh-keygen -t ed25519 -C "rnvcs-ci01-deploy" -f ~/.ssh/rnvcs_deploy_key -N ""
cat ~/.ssh/rnvcs_deploy_key.pub
```

Bu public key'i **her hedef sunucuda** `rnvcs-deploy` kullanıcısının `~/.ssh/authorized_keys`'ine ekleyin.

`deploy_apply.sh` ve `rollback.sh` `sudo` ile servis yeniden başlatıyor — `rnvcs-deploy` kullanıcısına, SADECE gereken komutlar için şifresiz sudo izni verin (her şeye değil):

```bash
# Hedef sunucuda: sudo visudo -f /etc/sudoers.d/rnvcs-deploy
rnvcs-deploy ALL=(root) NOPASSWD: /opt/rnvcs/scripts/deploy_apply.sh, /opt/rnvcs/scripts/rollback.sh, /opt/rnvcs/scripts/backup_current.sh, /usr/bin/systemctl restart rnvcs-yonetim-servisi, /usr/bin/systemctl restart rnvcs-panel-backend
```

Bu betikleri (`deploy_apply.sh`, `rollback.sh`, `backup_current.sh`, `rnvcs_smoke_test.sh`) her hedef sunucuya bir kere elle kopyalayın (`/opt/rnvcs/scripts/` altına) — bunlar CI'nin SSH ile çağırdığı, hedefte zaten duran betikler:

```bash
scp scripts/deploy_apply.sh scripts/rollback.sh scripts/backup_current.sh scripts/rnvcs_smoke_test.sh \
  rnvcs-deploy@15.2.4.11:/opt/rnvcs/scripts/
```
(Aynısını `15.2.4.10` ve gerekirse `15.2.4.201` için de yapın.)

`DEPLOY_SSH_KEY` secret'ına `~/.ssh/rnvcs_deploy_key`'in **private** içeriğini (`cat ~/.ssh/rnvcs_deploy_key`) koyun.

---

## 3. Günlük Kullanım — Artık Nasıl Çalışacak

**Geliştirme sırasında:**
```bash
git checkout -b feature/yeni-ozellik
# ... değişiklik yapın ...
git push origin feature/yeni-ozellik
# → CI otomatik çalışır, PR açtığınızda sonucu Gitea'da görürsünüz
```

**Sürüm çıkarırken:**
```bash
git checkout main && git pull
echo "1.6.1" > VERSION
git add VERSION docs/CHANGELOG.md
git commit -m "v1.6.1"
git tag v1.6.1
git push origin main --tags
# → release.yml otomatik tetiklenir: derler, paketler, YEDEK CORE'a dağıtır, smoke test çalıştırır
```

**Yedek CORE'da sorun yoksa, üretime almak için:**
Gitea arayüzü → **İşlemler → Deploy Production → Run Workflow** → `version: 1.6.1` yazıp onay kutusunu işaretleyip çalıştırın. Otomatik: DB yedeği alınır → tar üretime kopyalanır → kurulur → smoke test çalışır → **başarısızsa otomatik geri alınır.**

---

## 4. Kurulumda Karşılaşılan Tuzaklar (gerçekte yaşandı, not düşüldü)

- **`act_runner register --instance http://localhost:3000` KULLANMAYIN.** Job'lar ayrı bir Docker container'ı içinde çalışıyor; o container için `localhost` kendisi demek, Gitea'nın olduğu host değil. `git fetch` "Failed to connect to localhost port 3000" hatasıyla patlar. Çözüm: Gitea'nın gerçekten erişilebilir IP'sini kullanın (ör. `http://10.1.82.85:3000`). Bu makinenin `enp1s0` (internet) bacağı DHCP olduğu için IP değişirse runner'ı bu IP ile YENİDEN KAYDETMEK gerekir (`rm .runner` + tekrar `register`).
- **`config.yaml`'da `container: network: bridge` KOYMAYIN.** Bu ayar, act_runner'ın her iş için otomatik oluşturduğu izole ağı (job container + servis container'larının birbirini isimle bulabildiği ağ) devre dışı bırakır — `postgres` servis container'ı hostname ile çözülemez olur ("could not translate host name"). Doğrusu `network` alanını BOŞ bırakmak (`network: ""`) — bu, act_runner'ın varsayılan iş-başına-ağ davranışını korur ve `ci.yml`'deki `psql -h postgres ...` gibi servis-adıyla-erişim satırları çalışır.

## 5. Bilinen Sınırlamalar / Sonraki İyileştirmeler

- Gitea Actions'ın "environment protection rule" (GitHub'daki gibi onaylı ortam kapıları) desteği sınırlı olabilir sürüme göre — bu yüzden `deploy-production.yml`'i `workflow_dispatch` ile "elle tetikleme" olarak tasarladık, aynı güvenliği daha basit şekilde sağlıyor.
- Panel'e (`panel_app.html`) dağıtım şu an sadece `deploy-production.yml` içinde, üretim dağıtımıyla birlikte yapılıyor — panel'i bağımsız güncellemek isterseniz ayrı bir `deploy-panel.yml` (aynı `workflow_dispatch` deseniyle) eklenebilir.
- Bildirim (deploy başarılı/başarısız olduğunda e-posta/mesaj) şu an yok — pipeline sonucu sadece Gitea arayüzünde görünüyor. İsterseniz bir sonraki adımda basit bir webhook/e-posta adımı ekleriz.
- `act_runner` tek makinede (rnvcs-ci01) çalışıyor — bu makine düşerse CI de durur; şimdilik kabul edilebilir bir risk, ölçek büyürse ikinci bir runner düşünülebilir.
