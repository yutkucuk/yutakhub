# yutakhub

Co-op modlarını tek tıkla kuran ve güncel tutan küçük bir Windows uygulaması.

Şu an içindeki modlar:

| Oyun | Mod | Sürüm |
|---|---|---|
| 20 Minutes Till Dawn | Co-op | 0.9.4 |
| Noobs Are Coming | Online Co-op | 1.0.0 (oyun sürümü 1.3.2) |
| Halls of Torment | Co-op | 0.3.1 |

## Oyuncular için

1. [Son sürüm](../../releases/latest) sayfasından `yutakhub.exe` dosyasını indir ve çalıştır. Kurulum yok.
2. Windows "bilinmeyen yayıncı" uyarısı gösterirse "Ek bilgi" > "Yine de çalıştır" de (uygulama imzalı değil).
3. Oyunlar Steam kütüphanenden kendiliğinden bulunur. "Kur" ya da "Güncelle"ye bas, sonra "Oyna".

Birlikte oynayacağınız herkeste modun aynı sürümü kurulu olmalı. Modlar resmi değildir; oyunların geliştiricileriyle bir bağı yoktur.

English: download `yutakhub.exe` from the [latest release](../../releases/latest) and run it. It finds your Steam games, installs the co-op mods in one click and keeps them (and itself) up to date. The app is in Turkish and English.

## Nasıl çalışır

- Uygulama açılınca bu depodaki `catalog.json` dosyasını okur. Oyun listesi, mod sürümleri ve indirme adresleri oradadır; yani **yeni bir oyun ya da mod sürümü eklemek için uygulamayı güncellemek gerekmez.**
- Mod dosyaları `mods/` klasöründedir.
- Uygulamanın kendisi [Releases](../../releases) sayfasından güncellenir. `app/version.txt` içindeki numara artınca GitHub yeni exe'yi kendisi derleyip yayınlar; kullanıcılar uygulamanın üstünde "Uygulamayı güncelle" düğmesini görür.

## Bir modu güncellemek

1. Yeni paketi `mods/<oyun>/` klasörüne yükle (örnek: `mods/hot/hot-coop-0.3.2.zip`).
2. `catalog.json` içinde o modun `version` ve `url` alanlarını değiştir. `sha256` alanını ya yeni dosyanın SHA-256 değeriyle değiştir ya da boş bırak (`""`).
3. Kaydet. Birkaç dakika içinde herkesin yutakhub'ında "Güncelle" düğmesi çıkar.

## Uygulamanın yeni sürümünü çıkarmak

`app/version.txt` içindeki numarayı artır (örnek: `1.0.1`) ve kaydet. Gerisini "Actions" sekmesindeki iş akışı yapar.

## catalog.json alanları

Her oyun için: `id`, `name`, `steamAppId`, `exe` (oyun klasöründeki exe adı) ve bir `mod`.

`mod.type` iki türden biridir:

- **`extract`**: Bir zip, oyun klasörüne açılır.
  - `url`, `sha256`: paket ve (isteğe bağlı) doğrulama değeri.
  - `detect`: Modun kurulu olduğunu gösteren dosya.
  - `keep`: Varsa üzerine yazılmayacak dosyalar (oyuncunun ayarları).
  - `remove`: "Modu kaldır" denince silinecek dosya ve klasörler.
- **`patch`**: Oyunun bir dosyası yamalanır; oyunun kendi dosyası dağıtılmaz, yalnızca fark dağıtılır.
  - `target`: Yamalanacak dosya. `backup`: Orijinalin saklanacağı ad.
  - `patches`: Desteklenen her oyun sürümü için `from` (orijinal dosyanın SHA-256'sı), `to` (yamalı dosyanın SHA-256'sı), `url` (yama dosyası) ve `gameVersion`.
  - `knownModded`: Modun eski sürümlerinin ürettiği dosyaların SHA-256 değerleri. Mod güncellenince bir önceki `to` değerini buraya ekle ki eski kurulumlar "Güncelle" görsün.

Yama dosyası üretmek için: `yutakhub.exe mkpatch <orijinal dosya> <modlu dosya> <çıktı.yhp>`

`summary` ve `notes` alanları `tr` ve `en` olarak yazılır.

## Kendin derlemek

Go 1.22+ ile: `cd app && GOOS=windows GOARCH=amd64 go build -ldflags "-H windowsgui -X main.repo=KULLANICI/yutakhub" -o yutakhub.exe .`

`-X main.repo` verilmezse test derlemesi olur: mod listesini exe'nin yanındaki `catalog.json` ve `mods/` klasöründen okur.
