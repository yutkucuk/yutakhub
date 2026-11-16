# yutakhub

Co-op modlarını tek tıkla kuran ve güncel tutan bir Windows uygulaması.

| Oyun | Mod |
|---|---|
| 20 Minutes Till Dawn | Co-op |
| Noobs Are Coming | Online Co-op |
| Halls of Torment | Co-op |
| Wait, They Can Climb?! Demo | Co-op |

## Kurulum

1. [Son sürüm](../../releases/latest) sayfasından **`yutakhub-setup.exe`** dosyasını indir ve çalıştır.
2. Windows "bilinmeyen yayıncı" uyarısı gösterirse "Ek bilgi" > "Yine de çalıştır" de (uygulama imzalı değil).
3. "Kur"a bas. Yönetici izni gerekmez; Başlat menüsüne ve istersen masaüstüne kısayol eklenir.
4. Oyunlar Steam kütüphanenden kendiliğinden bulunur. "Kur" ya da "Güncelle"ye bas, sonra "Oyna".

Kaldırmak için: Windows Ayarlar > Uygulamalar > yutakhub > Kaldır.

Birlikte oynayacağınız herkeste modun aynı sürümü kurulu olmalı. Modlar resmi değildir; oyunların geliştiricileriyle bir bağı yoktur.

**English:** download `yutakhub-setup.exe` from the [latest release](../../releases/latest) and run it. yutakhub finds your Steam games, installs the co-op mods in one click and keeps them (and itself) up to date. The app is in Turkish and English.

## Nasıl çalışır

- Uygulama açılınca bu depodaki `catalog.json` dosyasını okur. Oyun listesi, mod sürümleri ve indirme adresleri oradadır; yeni bir oyun ya da mod sürümü için uygulamayı güncellemek gerekmez.
- Mod paketleri `mods/` klasöründedir.
- Uygulamanın kendisi [Releases](../../releases) sayfasından güncellenir.

## Geliştirici: bir modu yayınlamak

Uygulamanın içinde gizli bir **Yayınla** sekmesi var. Açmak için Hakkında sayfasında sürüm başlığına beş kez tıkla (ya da uygulamayı `--publisher` ile başlat).

1. Bir kereliğine: depo adını (`kullanıcı/yutakhub`) ve bir GitHub erişim anahtarını gir. Anahtar: GitHub > Settings > Developer settings > Fine-grained tokens; yalnızca bu depo, Contents: Read and write.
2. Modun yeni sürümü kendi bilgisayarındaki oyun klasöründe kurulu olsun.
3. Yayınla sekmesinde o mod "Yayınlanmamış değişiklik var" der. Sürümü ve notları kontrol edip **Yayınla**'ya bas.

Uygulama paketi oyun klasöründen kendisi toplar, GitHub'a yükler ve `catalog.json` dosyasını günceller. Birkaç dakika içinde herkesin yutakhub'ında "Güncelle" çıkar.

## Geliştirici: uygulamanın yeni sürümünü çıkarmak

`app/version.txt` içindeki numarayı artır (örnek: `1.0.1`) ve kaydet. "Actions" sekmesindeki iş akışı `yutakhub-setup.exe` dosyasını derleyip yayınlar; kurulu uygulamalar üstte "Uygulamayı güncelle" düğmesini gösterir.

## catalog.json alanları

Her oyun için: `id`, `name`, `steamAppId`, `exe` (oyun klasöründeki exe adı) ve bir `mod`.

`mod.type` iki türden biridir:

- **`extract`**: Bir zip, oyun klasörüne açılır.
  - `url`, `sha256`: paket ve doğrulama değeri. `content`: paket içeriğinin parmak izi (Yayınla sekmesi yazar).
  - `detect`: Modun kurulu olduğunu gösteren dosya.
  - `keep`: Varsa üzerine yazılmayacak dosyalar (oyuncunun ayarları).
  - `remove`: "Modu kaldır" denince silinecek dosya ve klasörler.
- **`patch`**: Oyunun bir dosyası yamalanır; oyunun kendi dosyası dağıtılmaz, yalnızca fark dağıtılır.
  - `target`: Yamalanacak dosya. `backup`: Orijinalin saklanacağı ad.
  - `patches`: Desteklenen oyun sürümü için `from` (orijinal dosyanın SHA-256'sı), `to` (yamalı dosyanın SHA-256'sı), `url` ve `gameVersion`.
  - `knownModded`: Modun eski sürümlerinin ürettiği dosyalar; eski kurulumların "Güncelle" görmesini sağlar (Yayınla sekmesi yazar).

`mod.publish`, Yayınla sekmesinin modu oyun klasöründen nasıl toplayacağını söyler:

- `include` / `exclude`: Pakete girecek ve girmeyecek dosyalar (`klasör/**` bir klasörün tamamı demektir).
- `versionFile`, `versionRegex`, `versionUtf16`: Mod sürümünün okunacağı dosya ve desen.
- `gameVersionRegex`: Yamalı modlarda oyun sürümünün okunacağı desen.

Yeni bir oyun eklemek için `catalog.json` içine yeni bir oyun bloğu yaz; ilk paketi de Yayınla sekmesi yükler.

`summary` ve `notes` alanları `tr` ve `en` olarak yazılır.

## Kendin derlemek

Go 1.22+ ile: `cd app && GOOS=windows GOARCH=amd64 go build -ldflags "-H windowsgui -X main.repo=KULLANICI/yutakhub" -o yutakhub.exe .`

`-X main.repo` verilmezse test derlemesi olur: mod listesini exe'nin yanındaki `catalog.json` ve `mods/` klasöründen okur, kurulum istemez ve kendini güncellemez.
