# zpkg

ZIGNOS のソースベースパッケージマネージャ(Go 単一バイナリ)。KISS Linux の `kiss` を
出発点に、USE フラグ・スロット・binpkg・トランザクション・リリースチャンネル・
再現可能ビルドまで実装した **v0.2.0**。
設計の詳細は zignos リポジトリの `docs/design-pkg.md` を参照。

## ビルドとテスト

```sh
go build -o zpkg ./cmd/zpkg
go vet ./...
go test ./...
```

## クイックスタート(テストレシピ)

```sh
export ZPKG_ROOT=/tmp/zpkg-test
export ZPKG_REPOS="$PWD/testdata/repos/core"
go build -o zpkg ./cmd/zpkg

./zpkg install hello2        # 依存 hello を先にビルド→インストール
$ZPKG_ROOT/bin/hello2
./zpkg list
./zpkg remove hello2 && ./zpkg remove hello

./zpkg install dl-test       # 実ダウンロード + sha256 検証 + 展開の経路テスト
./zpkg remove dl-test
./zpkg install hello && ./zpkg install hello-conflict   # "bin/hello: already owned by hello" で失敗
./zpkg remove hello
```

インストール時は emerge 風のフェーズ表示になる:

```
These are the packages that will be installed, in order:
  [1] hello-1.0.0 (dependency)  USE=""
  [2] hello2-1.0.0              USE=""

Total: 2 packages (1 target, 1 dependency)

>>> Building hello-1.0.0
>>> Built hello-1.0.0 (2 files) in 100ms
>>> Installing (1 of 2) hello-1.0.0
>>> Installed hello-1.0.0
...
```

注意: Windows の開発ホストでは `ZPKG_REPOS` の区切りは `;`(Unix は `:`)。

## コマンド

| コマンド | 動作 |
|---|---|
| `depends [-r] <pkg>...` | 依存(と makedepends)を含むビルド順。`-r` はインストール済みの逆依存を表示 |
| `build [-f] [--no-sandbox] [--clean] [--repro-check] <pkg>...` | fetch → 検証 → destroot まで(インストールしない)。build は既定で bubblewrap 内。`--clean` は work を削除、`--repro-check` は 2 回ビルドして manifest 比較 |
| `install [-f] [-p] [--asdeps] [--no-sandbox] [--clean] [--repro-check] <pkg>...` | 必要ならビルドして `$ZPKG_ROOT` へ。`-p` は pretend、`--asdeps` は world に追加しない。`--clean` / `--repro-check` はキャッシュを使わずソースビルド |
| `upgrade [-p] [-y] [--newuse] [--prune] [--allow-downgrade] [--channel <ch>] [--verify] [pkg]...` | world(または指定 pkg)を選択チャンネルの版へ。変更一覧・依存差分・警告を表示し、全ビルド成功後にインストール。`--newuse` は USE 変更時に再ビルド、`--prune` は孤立依存を削除、`--allow-downgrade` はダウングレードを許可、`--verify` は全ファイルの sha256 を検証 |
| `remove [-f] <pkg>...` | 全スロットを削除し world から除去。逆依存がある場合は停止(`-f` で強制) |
| `list` | インストール済み一覧(`name@slot` 表示) |
| `info <pkg>...` | インストール済みメタデータとレシピの版 |
| `owner <path>...` | パスの所有パッケージ |
| `world` | 明示インストール一覧 |
| `search <term>` | レシピ検索 |
| `use [flag\|-flag]...` / `use --package <pkg> ...` | USE フラグの表示・編集(グローバル / package.use) |
| `mkindex [--key <private>] [--generation N] [--expires ...] [dir]` | binpkg リポジトリ用 `index.json` を生成。`--key` で署名し `keys/<keyid>.pub` も出力 |
| `sync [--from <src>] [--dest <dir>] [-n] [--allow-unsigned]` | レシピツリー全体を stage3/レシピ tarball・ディレクトリ・URL から置換。署名付き `recipes.manifest` を検証 |
| `key generate --private <path> [--public <path>]` | Ed25519 署名鍵ペアを生成(秘密鍵 0600) |
| `key import <pubkey>...` | 公開鍵を trusted.d に登録 |
| `key list` / `key remove <keyid>` | 信頼済み鍵の一覧 / 削除 |
| `sign --key <private> <file>...` | `<file>.sig`(detached Ed25519 署名)を生成 |
| `verify <file>...` | `<file>.sig` を trusted.d の鍵で検証 |
| `sign-recipes --key <private> [--generation N] [--expires ...] [dir]` | レシピツリーの `recipes.manifest` と署名を生成 |
| `provenance <pkg>...` | 記録済みのビルド来歴(JSON)を表示 |
| `sbom <pkg>...` / `sbom --system [-o <file>]` | SPDX 2.3 SBOM の表示・出力 |
| `log [-p] <pkg>...` | ビルドログの表示(`-p` はパスだけ) |
| `channel [stable\|testing\|unstable]` | 実効チャンネルと利用可能なツリーを表示。引数付きで `/etc/zpkg/make.conf` の `CHANNEL=` を更新 |
| `recover` | 中断したトランザクションの修復(install はロールバック、remove は完了) |

`-f` は「インストール済みでも処理する」フラグで、**binpkg キャッシュは無視しない**。
同じ version+revision+実効 USE の binpkg があればそれを使うので、レシピ内容を
変えたときは `revision` を上げる。`--clean` / `--repro-check` は明示的にソース
ビルドするため、キャッシュ済み binpkg を使わない。

## レシピツリーの更新

stage3 に同梱される `/usr/share/zignos/recipes` はスナップショットなので、
新しいパッケージを使うにはレシピツリーを更新する。`sync` は新しいツリーを
一時領域で検証してから原子的に置き換える(失敗時は旧ツリーを維持)。

```sh
# 新しい stage3 tarball から(レシピ部分だけを展開する)
zpkg sync --from /path/to/zignos-stage3-20261119-r6.tar.xz

# レシピだけの tarball / 展開済みディレクトリ / URL も可
zpkg sync --from /path/to/zignos-recipes.tar.xz
zpkg sync --from ./zignos-recipes
zpkg sync --from https://example.invalid/zignos-recipes.tar.xz

# 検証だけ
zpkg sync --from ./zignos-recipes -n
```

`--from` を省略すると `ZPKG_RECIPE_URL`、次に `/etc/zpkg/sync.conf` の
`RECIPEURL=` を使う。`--dest` で置換先を変更できる(既定:
`$ZPKG_ROOT/usr/share/zignos/recipes`)。stage3 の tarball を渡した場合、
`usr/share/zignos/recipes` 以外は展開しない。

`sync` は新しいツリーの `recipes.manifest` を trusted key で検証してから
置換する。署名がない場合は既定で拒否し、開発時のみ `--allow-unsigned`
(または `ZPKG_ALLOW_UNSIGNED=1`)で許可する。署名が存在して壊れている
場合は `--allow-unsigned` でも拒否する。

チャンネルツリー(`stable/` / `testing/` / `unstable/` の下に `core/` と
`main/`)も同じコマンドで同期できる。stage3 は stable だけを同梱するため、
testing / unstable を使う場合は全チャンネル入りのレシピ tarball を
`zpkg sync` で取り込む。従来のフラットツリー(`core/` / `main/` 直下)も
stable として引き続き受理する。

## リリースチャンネル

ZIGNOS のレシピツリーは3つのチャンネルを持つ。

| チャンネル | 用途 |
|---|---|
| `stable` | 安定版。既定。stage3 / イメージに同梱 |
| `testing` | 次期 stable の検証 |
| `unstable` | 開発中の最新版 |

選択は `/etc/zpkg/make.conf` の `CHANNEL=` で行う(既定 `stable`)。

```ini
# /etc/zpkg/make.conf
CHANNEL=testing
CFLAGS="-O2 -pipe"
```

`ZPKG_CHANNEL` 環境変数は1回の実行だけ上書きする。`zpkg upgrade --channel`
も同じで、設定ファイルは変更しない。永続的に切り替える場合は
`zpkg channel testing` を使う(他の設定行は保持される)。

```sh
zpkg channel                  # 実効値と利用可能チャンネル
zpkg channel testing          # make.conf を更新
zpkg upgrade -p               # 差分の確認
zpkg upgrade                  # 実行(確認プロンプトあり)
```

チャンネルごとにレシピの version / revision は独立して上げる。**同じ
version+revision のまま内容だけ変えると binpkg キャッシュが衝突する**ため、
内容を変えたら必ず revision を上げる。チャンネル間で版が同じパッケージは
`zpkg upgrade` では再インストールされない(`zpkg install -f` で強制)。

### upgrade の表示と安全性

`zpkg upgrade` は実行前に次を表示する。

- アップグレード / 新規依存 / 再ビルド / ダウングレードの一覧
- 依存関係の追加・削除・制約変更
- レシピの `upgrade_warning`、主要パッケージのメジャー版本変更、
  新しい conflict、slot 変更、チャンネル変更
- 以前の依存関係にあり、新しい closure では不要になったパッケージ(孤立)。既定では残し、`--prune` で削除
- 合計件数

その後、確認プロンプトを出す。ダウングレードを含む場合は
`--allow-downgrade` が無ければ専用の確認(`[y/N]`)を出す。`--yes` は
通常の確認を省略する。非対話環境では `--yes` が必要。

計画したパッケージは**すべてビルドしてから**インストールする。ビルドが
1つでも失敗すればシステムは変更されない。インストール失敗時はその
パッケージだけがロールバックされ、再実行で続きから再開できる。クラッシュ
した場合は `zpkg recover` が処理する。完了後は依存関係を再解決し、
変更したパッケージの manifest を検証する(`--verify` で全ファイルの
sha256 も照合)。

## 署名と検証

配布物は Ed25519 の detached signature で検証する。外部の GPG や
`golang.org/x/crypto` には依存しない。

### 鍵

```sh
# リポジトリ/レシピ署名用の鍵ペア(秘密鍵は 0600、既存ファイルは上書きしない)
zpkg key generate --private ~/zignos-signing.key --public ~/zignos-signing.pub

# クライアント: 公開鍵を信頼する(フィンガープリントは別経路で確認する)
zpkg key import zignos-signing.pub
zpkg key list
```

- key ID = Ed25519 公開鍵 32 バイトの SHA-256(64 hex)
- 信頼済み鍵: `$ZPKG_ROOT/etc/zpkg/trusted.d/*.pub`(既定)
- TOFU はしない。`key import` した鍵だけを信頼する
- 秘密鍵が group/other から読める場合、Unix では読み込みを拒否する
- 秘密鍵はレシピツリーや binrepo の中に置かない(`mkindex` / `sign-recipes`
  が拒否する)

### binpkg リポジトリ

推奨レイアウト(従来のフラット配置も読める):

```
binrepo/
├── index.json
├── index.json.sig
├── keys/<keyid>.pub
└── packages/
    ├── gcc.pkg.tar.gz
    ├── gcc.manifest
    └── gcc.meta.json
```

```sh
zpkg mkindex --key ~/zignos-signing.key --generation 42 --expires 90d /srv/binrepo
```

`index.json`(schema 2)は `generation` / `expires` / `keyid` を持ち、
`index.json.sig` がその実バイト列への署名になる。各エントリの
`manifest_sha256` も検証する。クライアントは trusted key による署名、
期限、前回見た generation との比較を行い、改ざん・期限切れ・巻き戻しを
ハードエラーにする(ソースビルドへ黙って逃げない)。

### レシピツリー

```sh
zpkg sign-recipes --key ~/zignos-signing.key --generation 42 --expires 90d .
```

`recipes.manifest` は `.git` と署名自身を除く全 regular file の
path / sha256 / size / mode を列挙する。`sync` は manifest の署名、
全ファイルのハッシュ、manifest にない余計なファイルを検証する。
stage3 に同梱されたツリーでも同じ検証ができる。

### ロールバック対策と期限

- `generation` は単調増加。省略時は既存の index/manifest の +1
- `expires` は RFC3339 または `90d` / `720h`。既定 +90 日
- 最後に受理した generation と SHA-256 を
  `$ZPKG_DB/.security.json` に保存する
- 新しい generation が保存値より小さい → ロールバックとして拒否
- 同じ generation で内容が違う → すり替えとして拒否
- 期限切れ → 拒否。`ZPKG_ALLOW_EXPIRED=1` は期限だけ解除する
  (署名・ロールバック検証は残る)
- `--allow-unsigned` は署名が**存在しない**場合だけ許可する。壊れた署名を
  受け入れる抜け道にはならない

### 検証ポリシー

```ini
# /etc/zpkg/security.conf
TRUSTED_KEYS=/etc/zpkg/trusted.d
ALLOW_UNSIGNED=0
ALLOW_EXPIRED=0
```

環境変数 `ZPKG_TRUSTED_KEYS` / `ZPKG_ALLOW_UNSIGNED` /
`ZPKG_ALLOW_EXPIRED` が設定ファイルより優先される。

## ビルド環境の隔離

レシピの `build` は任意のコマンドを実行できるため、既定で **bubblewrap** の
サンドボックス内で実行する。

- root 全体は読み取り専用(`--ro-bind / /`)。書き込みできるのは
  `$ZPKG_CACHE/build/<pkg>` だけ
- `/tmp`、`/run`、`/var/tmp`、`/var/lib`、`/var/log`、`/root`、`/home`、
  `/mnt`、`/media`、`/srv` は空の tmpfs
- `/etc/ssh`、`/etc/ssl/private`、`/etc/zpkg`、`/etc/NetworkManager/system-connections`、
  `/etc/wpa_supplicant` はマスク。`/etc/shadow`、`/etc/gshadow`、`/etc/sudoers`、
  `/etc/doas.conf`、`/etc/machine-id` は `/dev/null`
- PID / IPC / UTS namespace を分離し、hostname は `zignos-build`
- capability は `CAP_CHOWN` / `CAP_FOWNER` / `CAP_DAC_OVERRIDE` / `CAP_FSETID` /
  `CAP_SYS_CHROOT` のみ(`doas`/`sudo` の chown、setuid、`zignos-mkinitramfs` 用)。
  `CAP_SYS_ADMIN` はないので読み取り専用 root を remount できない
- 環境変数は allowlist(`PATH` / `HOME` / `TMPDIR` / `LANG` / `LC_ALL` /
  `GOCACHE` / `GOPATH` / `ZPKG_*` など)。`http_proxy` 系だけ引き継ぐ
- `ZPKG_ROOT=/` かつ Linux のときだけ適用。それ以外は `auto` なら警告して
  直接ビルド、`require` ならエラー

```sh
ZPKG_SANDBOX=auto      # 既定: bwrap があれば使い、無ければ警告して直接ビルド
ZPKG_SANDBOX=require   # bwrap が無い/使えない場合は失敗
ZPKG_SANDBOX=off       # サンドボックスなし
zpkg build --no-sandbox <pkg>
```

`/etc/zpkg/sandbox.conf`:

```ini
SANDBOX=auto
BUILD_NET=1
#BWRAP=/usr/bin/bwrap
```

### ネットワーク

ビルド時のネットワークは既定で許可する。再現性を重視する場合は
`BUILD_NET=0` / `ZPKG_BUILD_NET=0` で拒否でき、`--unshare-net` が付く。

レシピの `build_network` ファイル(`1` / `0`)がグローバル設定を上書きする。
Go モジュールを取得する `age` / `fzf` / `restic` は `1` を明示している。

### サンドボックスと chroot

bubblewrap の `pivot_root` は chroot 先が mount point であることを要求する。
`zignos-enter.sh` は private mount namespace を作り、root を bind mount してから
chroot するため、stage3 のディレクトリ chroot でもサンドボックスが動く。
素の `chroot` で入った場合は `auto` が警告して直接ビルドにフォールバックする。

ビルド環境の fingerprint(`name@slot-version` の sha256 と件数)は
`meta.json` と binpkg sidecar に記録され、`zpkg info` で表示される。
サンドボックス内に見えるのはインストール済みシステム全体(読み取り専用)で、
makedepends だけの最小ルートではない。

## 再現可能なビルド

build は `SOURCE_DATE_EPOCH`(既定 `946684800` = 2000-01-01T00:00:00Z)を
基準に実行される。`ZPKG_SOURCE_DATE_EPOCH` → `SOURCE_DATE_EPOCH` →
`/etc/zpkg/build.conf` の順で上書きできる。build 環境には
`SOURCE_DATE_EPOCH`、`KBUILD_BUILD_TIMESTAMP` / `KBUILD_BUILD_USER=zignos` /
`KBUILD_BUILD_HOST=zignos-build`、`PYTHONHASHSEED=0` が渡り、レシピは
`umask 022` の wrapper 経由で実行される。

binpkg の gzip header と tar 全エントリの mtime は SOURCE_DATE_EPOCH に固定
される(所有者・モード・内容・順序はビルドのまま)。work ディレクトリは
`$ZPKG_CACHE/build/<name>-<version>[-rN][-opts]`。

### ビルドログ

既定で `$ZPKG_CACHE/logs/<base>.log` に stdout/stderr を保存する。ヘッダには
package / recipe / recipe digest / epoch / sandbox / network / started /
build script が入り、失敗したビルドも残る。`BUILD_LOG=0` または
`ZPKG_BUILD_LOG=0` で無効。`zpkg log <pkg>` で表示、`-p` でパスだけ表示する。

### provenance

ビルド・インストール時に `provenance.json`(schema 1)を生成する。

- `$ZPKG_DB/<name>[@slot]/provenance.json`
- `$ZPKG_CACHE/binpkgs/<base>.build.json`

記録内容: package / recipe(repo・dir・全ファイルの digest・署名 key/generation)/
sources(URL・file・sha256・size)/ build(USE・CFLAGS・MAKEOPTS・sandbox・
network・SOURCE_DATE_EPOCH・started/finished・origin)/ toolchain
(gcc/binutils/glibc/make/bash/go/zpkg/bubblewrap)/ dependencies /
build_dependencies / environment(インストール済み全パッケージと hash)/
artifact(filename・sha256・size・manifest_sha256)/ log(path・sha256)。

```sh
zpkg provenance zlib
```

binpkg / binrepo からのインストールは、判明する範囲(origin、artifact hash、
sidecar の recipe digest、ビルド環境)だけを記録する。

### SPDX 2.3 SBOM

- `$ZPKG_DB/<name>[@slot]/sbom.json`
- `$ZPKG_CACHE/binpkgs/<base>.spdx.json`

target に artifact の SHA-256 と purl(`pkg:generic/zignos/<name>@<version>`)、
runtime 依存に `DEPENDS_ON`、makedepends に `BUILD_DEPENDENCY_OF`、toolchain に
`BUILD_TOOL_OF` を張る。system SBOM は合成 `SPDXRef-System-zignos` に全パッケージの
`CONTAINS` を持つ。ライセンスはレシピが持たないため `NOASSERTION`。

```sh
zpkg sbom zlib
zpkg sbom --system -o system.spdx.json
```

`SBOM=0` / `ZPKG_SBOM=0` で生成を無効にできる。

### 再現性検証

```sh
zpkg build --repro-check <pkg>
```

2 回ビルドし、destroot の manifest(Kind / Path / Hash / Link)を比較する。
差分は path ごとに表示して失敗し、一致すれば
`Reproducible: <pkg> (manifest matches, artifact <sha256>)` を表示する。
binpkg のバイト一致は保証せず、hash が異なる場合は注記する。

## 環境変数

| 変数 | 既定 | 内容 |
|---|---|---|
| `ZPKG_REPOS` | 同梱ツリー | レシピリポジトリ。OS のパス区切りで複数指定(Unix `:` / Windows `;`)。先頭優先 |
| `ZPKG_ROOT` | `/` | ターゲットルート |
| `ZPKG_CACHE` | `$ZPKG_ROOT/var/cache/zpkg` | ソースキャッシュ・ビルド作業領域・binpkg |
| `ZPKG_DB` | `$ZPKG_ROOT/var/lib/zpkg` | インストール済み DB |
| `ZPKG_FORCE` | なし | `1` で改変済みパッケージの remove を許可 |
| `ZPKG_NO_BINPKG` | なし | `1` でローカル/リポジトリの binpkg を無効化 |
| `ZPKG_BINREPO` | なし | binpkg リポジトリの URL/パス(`/etc/zpkg/binrepo.conf` の `BINREPO=` でも可) |
| `ZPKG_RECIPE_URL` | なし | `zpkg sync` の既定ソース(URL/パス)。`/etc/zpkg/sync.conf` の `RECIPEURL=` でも可 |
| `ZPKG_TRUSTED_KEYS` | `$ZPKG_ROOT/etc/zpkg/trusted.d` | 信頼する `*.pub` のディレクトリ(`security.conf` の `TRUSTED_KEYS=` でも可) |
| `ZPKG_ALLOW_UNSIGNED` | なし | `1` で署名のない index/レシピツリーを許可(開発用。壊れた署名は不可) |
| `ZPKG_ALLOW_EXPIRED` | なし | `1` で署名の期限切れだけ許可(復旧用) |
| `ZPKG_SANDBOX` | `auto` | `auto` / `require` / `off`。build の bubblewrap 隔離 |
| `ZPKG_CHANNEL` | `stable` | `stable` / `testing` / `unstable`。`make.conf` の `CHANNEL=` を1回だけ上書き |
| `ZPKG_BUILD_NET` | `1` | `0` でビルドを network namespace 内に隔離 |
| `ZPKG_BWRAP` | `bwrap` | bubblewrap の実行ファイル |
| `ZPKG_USE` / `ZPKG_CFLAGS` / `ZPKG_MAKEOPTS` | ファイル | USE とビルドフラグの上書き(空文字も「明示的に空」) |
| `ZPKG_SOURCE_DATE_EPOCH` | `946684800` | 固定ビルド時刻。標準の `SOURCE_DATE_EPOCH` も受け付ける |
| `ZPKG_BUILD_LOG` | `1` | `0` でビルドログを保存しない |
| `ZPKG_SBOM` | `1` | `0` で SPDX SBOM を生成しない |

設定ファイル(ターゲットルート配下):

- `/etc/zpkg/use.conf` — グローバル USE フラグ
- `/etc/zpkg/package.use` — パッケージ単位の USE 上書き
- `/etc/zpkg/make.conf` — `CFLAGS=` / `MAKEOPTS=` / `CHANNEL=stable|testing|unstable`
- `/etc/zpkg/binrepo.conf` — `BINREPO=`
- `/etc/zpkg/sync.conf` — `RECIPEURL=`(`zpkg sync` の既定ソース)
- `/etc/zpkg/security.conf` — `TRUSTED_KEYS=` / `ALLOW_UNSIGNED=` / `ALLOW_EXPIRED=`
- `/etc/zpkg/sandbox.conf` — `SANDBOX=` / `BUILD_NET=` / `BWRAP=`
- `/etc/zpkg/build.conf` — `SOURCE_DATE_EPOCH=` / `BUILD_LOG=` / `SBOM=`

## レシピ形式(ディレクトリ型・KISS 式)

```
<repo>/<pkg>/
├── version       # 1 行。必須
├── sources       # 1 行 1 URL。省略可
├── checksums     # 1 行 1 sha256。sources と同数・同順
├── depends       # 実行時依存(1 行 1 個、flag?pkg 可)
├── makedepends   # ビルド時のみの依存(flag?pkg 可)
├── revision      # -rN。内容を変えたら上げる
├── slot          # 共存スロット(既定 0)
├── iuse          # 理解する USE フラグ
├── provides / conflicts
├── build_network # 1/0 でビルド時ネットワークを上書き(省略時はグローバル)
├── upgrade_warning # 任意。upgrade 前に表示する注意メッセージ
├── build         # bash スクリプト。必須。恒久的に shell
├── post_install  # 任意のフック
└── post_remove   # 任意のフック
```

空行と `#` コメントは無視。`build` は `bash` で実行され、`$ZPKG_DESTROOT` に
成果物を置く契約(`make DESTDIR="$ZPKG_DESTROOT" install` 等)。渡される環境:
`ZPKG_NAME` / `ZPKG_VERSION` / `ZPKG_RECIPE_DIR` / `ZPKG_ROOT` / `ZPKG_CACHE` /
`ZPKG_DESTROOT` / `ZPKG_SRC` / `ZPKG_WORKDIR` / `ZPKG_USE` / `ZPKG_CFLAGS` /
`ZPKG_MAKEOPTS` / `ZPKG_SANDBOX` / `ZPKG_BUILD_NET`。再現性のため
`SOURCE_DATE_EPOCH` / `KBUILD_BUILD_TIMESTAMP` / `KBUILD_BUILD_USER` /
`KBUILD_BUILD_HOST` / `PYTHONHASHSEED=0` も渡す。レシピは `umask 022` で実行される。

- `sources` の URL に `#ファイル名` を付けるとキャッシュ名を指定できる
- `.patch` / `.diff` は展開されず `$ZPKG_SRC` に置かれる
- 空白を含むパスもマニフェストに記録できる(Go 文字列クォート形式)

## 依存アトムと解決

`depends` / `makedepends` / `provides` / `conflicts` は次のアトムを書ける。

| 例 | 意味 |
|---|---|
| `foo` | 任意バージョンの `foo` |
| `>=foo-1.2` / `<foo-2.0` / `=foo-1.2-r3` | バージョン制約(`>`, `<=`, `!=` も可) |
| `ssl?openssl` | `ssl` が有効なときだけ `openssl` |
| `!ssl?gnutls` | `ssl` が無効なときだけ `gnutls` |
| `foo[ssl]` / `foo[-ssl]` | 依存先の `ssl` が有効 / 無効であること |
| `foo[ssl?]` | 依存先が `ssl` を知っている場合だけ有効であること |
| `ssl?foo[ssl,-zlib]` | 条件と USE 制約の組み合わせ |

- バージョン制約は演算子付きで書く。`foo-1.2` はパッケージ名として扱う
  (名前に `-` と数字を含むパッケージがあるため)
- `provides` は `virtual/mailer` または `virtual/mailer-1.2`。バージョン省略時は
  提供側パッケージのバージョンを使う
- `conflicts` はバージョン制約・virtual 名も書ける。選択済みパッケージと
  インストール済みパッケージの両方に対して検査する
- 条件フラグは依存元レシピの `iuse` に存在する必要がある
- リゾルバは決定的なバックトラック方式:
  - 直接レシピと `provides` の候補を、インストール済み → 直接名 → 名前順で試す
  - バージョン / USE 制約を満たさない候補と、conflict する候補を除外
  - 失敗したら別の provider / 候補へ戻る
  - `virtual/foo` を install すると provider を選び、provider を world に記録する
- `zpkg depends -r <pkg>` はインストール済みの逆依存を表示する。
  `zpkg remove` は逆依存があると停止し、`-f` で強制できる
- **SAT solver は未実装**。現在はバックトラック解決で、候補選択の API は
  将来 SAT バックエンドへ差し替えられるよう `internal/resolve` に分離している
- 制限: レシピは名前ごとに 1 バージョン(複数バージョン/スロットは解決対象外)。
  逆依存はインストール済み DB を基準にする

## 実装済みの機能

- 依存解決: 依存アトムのバックトラック解決(バージョン制約、provides、conflicts、
  条件依存、USE 制約)。依存が先のトポロジカルソート、循環はエラー
- USE フラグ: `use.conf` / `package.use` / 環境変数、`iuse` による条件付き依存
  (`flag?pkg` / `!flag?pkg`)の検証、実効 USE の binpkg キーへの反映
- binpkg: ローカルキャッシュ(`binpkgs/`)とリポジトリ(`ZPKG_BINREPO` + `mkindex`)。
  一致条件は name / arch / version+revision / 実効 USE ハッシュ
- 署名: Ed25519 の detached signature。binrepo の `index.json` とレシピツリーの
  `recipes.manifest` を署名し、trusted key(`/etc/zpkg/trusted.d`)、
  ロールバック対策(`generation` + `.security.json`)、期限(`expires`)を検証する。
  インストール済みメタデータに origin / 署名鍵 / generation を記録
- ビルド隔離: bubblewrap による読み取り専用 root、書き込み可能な work のみ、
  PID/IPC/UTS namespace、capability 制限、環境変数 allowlist、任意の
  network namespace 分離。`auto` / `require` / `off` と `--no-sandbox`。
  build env の fingerprint を `meta.json` に記録
- 再現可能ビルド: `SOURCE_DATE_EPOCH`(既定 2000-01-01)と KBUILD 変数、
  `umask 022`、binpkg の tar/gzip mtime 正規化、work dir のパッケージ単位化、
  ビルドログ(`$ZPKG_CACHE/logs/`)、`provenance.json`、SPDX 2.3 SBOM、
  `--repro-check`(2 回ビルドの manifest 比較)
- slots: `name@slot` レコード。別スロットとのファイル重複はエラー
- world / upgrade: 明示インストール集合の追跡、`--newuse`、upgrade 時の
  不要ファイル削除(内容一致時のみ、ロールバック可能)
- リリースチャンネル: `stable` / `testing` / `unstable` の選択
  (`make.conf` / `ZPKG_CHANNEL` / `--channel`)。`zpkg channel` で切替。
  upgrade 前に変更一覧・依存差分・警告・孤立パッケージを表示し、
  全ビルド成功後にインストール。`--prune`、`--allow-downgrade`、
  更新後の整合性チェック(`--verify` で全ファイル照合)に対応
- トランザクション: `.txn/journal.json`(ヘッダ)+ `.txn/journal.log`
  (追記専用イベント、fsync 付き)。失敗時はその場でロールバック、
  クラッシュ時は次回操作または `zpkg recover` で修復
- 出力: 事前プラン(USE + Total)と `>>> Building` / `>>> Installing (i of n)` /
  `>>> Installed` のフェーズ行。端末のときだけ緑(`NO_COLOR` / `TERM=dumb` で無効)

## 既知の制限

- SAT solver / 複数バージョン・スロットを考慮した完全なソルバーは未実装
  (現在は決定的バックトラック。1 名 1 バージョンのレシピツリーが前提)
- 公開 binpkg リポジトリの運用・鍵配布は未整備(署名の仕組みと `keys/` 出力は実装済み)
- 鍵失効リスト(CRL)は未対応。`key remove` によるローテーションで運用する
- GPG/OpenPGP は未対応。署名 envelope の `algorithm` を拡張して将来追加する
- Secure Boot(GRUB/kernel の署名)は別問題で未対応
- 取得は http/https のみ(署名検証は http でも行うが、https を推奨)
- ビルド隔離は bubblewrap のみ。`post_install` / `post_remove` フック、
  ソース tarball の展開、CPU/メモリ/ディスクの resource limit は対象外
- サンドボックス内に見えるのはインストール済みシステム全体(読み取り専用)。
  makedepends だけの最小ビルドルートは未実装
- `--repro-check` は destroot manifest の比較で、tar のバイト一致・所有者・
  mtime は保証しない。上流が時刻・ホスト名・パスを埋め込むビルドは
  SOURCE_DATE_EPOCH だけでは一致しないことがある
- SBOM のライセンス / copyright はレシピに情報がないため `NOASSERTION`
- ビルドログは cache 配下なので、cache を消すと失われる
- upgrade はセット全体を1トランザクションでは扱わない。ビルド全成功後に
  インストールし、失敗時はパッケージ単位でロールバックする。再実行で再開、
  クラッシュは `zpkg recover` で復旧する
- チャンネル間で version+revision が同じパッケージは upgrade で再インストール
  されない(内容を変えたら revision を上げる。強制は `install -f`)
- 孤立パッケージの検出は slot 0 のみ。複数スロットを持つパッケージは
  `--prune` の対象外
- 素の `chroot` では bwrap の `pivot_root` が使えないため、`auto` は警告して
  直接ビルドにフォールバックする。stage3 では `zignos-enter.sh` を使う
