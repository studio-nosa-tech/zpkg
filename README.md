# zpkg

ZIGNOS のソースベースパッケージマネージャ(Go 単一バイナリ)。KISS Linux の `kiss` を
出発点に、USE フラグ・スロット・binpkg・トランザクションまで実装した **v0.9**。
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
| `build [-f] <pkg>...` | fetch → 検証 → destroot まで(インストールしない) |
| `install [-f] [-p] [--asdeps] <pkg>...` | 必要ならビルドして `$ZPKG_ROOT` へ。`-p` は pretend、`--asdeps` は world に追加しない |
| `upgrade [-p] [--newuse] [pkg]...` | world(または指定 pkg)をレシピの版へ。`--newuse` は USE 変更時に再ビルド |
| `remove [-f] <pkg>...` | 全スロットを削除し world から除去。逆依存がある場合は停止(`-f` で強制) |
| `list` | インストール済み一覧(`name@slot` 表示) |
| `info <pkg>...` | インストール済みメタデータとレシピの版 |
| `owner <path>...` | パスの所有パッケージ |
| `world` | 明示インストール一覧 |
| `search <term>` | レシピ検索 |
| `use [flag\|-flag]...` / `use --package <pkg> ...` | USE フラグの表示・編集(グローバル / package.use) |
| `mkindex [dir]` | binpkg リポジトリ用 `index.json` を生成 |
| `sync [--from <src>] [--dest <dir>] [-n]` | レシピツリー全体を stage3/レシピ tarball・ディレクトリ・URL から置換。`-n` は検証のみ |
| `recover` | 中断したトランザクションの修復(install はロールバック、remove は完了) |

`-f` は「インストール済みでも処理する」フラグで、**binpkg キャッシュは無視しない**。
同じ version+revision+実効 USE の binpkg があればそれを使うので、レシピ内容を
変えたときは `revision` を上げる。

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
| `ZPKG_USE` / `ZPKG_CFLAGS` / `ZPKG_MAKEOPTS` | ファイル | USE とビルドフラグの上書き(空文字も「明示的に空」) |

設定ファイル(ターゲットルート配下):

- `/etc/zpkg/use.conf` — グローバル USE フラグ
- `/etc/zpkg/package.use` — パッケージ単位の USE 上書き
- `/etc/zpkg/make.conf` — `CFLAGS=` / `MAKEOPTS=`
- `/etc/zpkg/binrepo.conf` — `BINREPO=`
- `/etc/zpkg/sync.conf` — `RECIPEURL=`(`zpkg sync` の既定ソース)

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
├── build         # bash スクリプト。必須。恒久的に shell
├── post_install  # 任意のフック
└── post_remove   # 任意のフック
```

空行と `#` コメントは無視。`build` は `bash` で実行され、`$ZPKG_DESTROOT` に
成果物を置く契約(`make DESTDIR="$ZPKG_DESTROOT" install` 等)。渡される環境:
`ZPKG_NAME` / `ZPKG_VERSION` / `ZPKG_RECIPE_DIR` / `ZPKG_ROOT` / `ZPKG_CACHE` /
`ZPKG_DESTROOT` / `ZPKG_SRC` / `ZPKG_WORKDIR` / `ZPKG_USE` / `ZPKG_CFLAGS` /
`ZPKG_MAKEOPTS`。

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
- slots: `name@slot` レコード。別スロットとのファイル重複はエラー
- world / upgrade: 明示インストール集合の追跡、`--newuse`、upgrade 時の
  不要ファイル削除(内容一致時のみ、ロールバック可能)
- トランザクション: `.txn/journal.json`(ヘッダ)+ `.txn/journal.log`
  (追記専用イベント、fsync 付き)。失敗時はその場でロールバック、
  クラッシュ時は次回操作または `zpkg recover` で修復
- 出力: 事前プラン(USE + Total)と `>>> Building` / `>>> Installing (i of n)` /
  `>>> Installed` のフェーズ行。端末のときだけ緑(`NO_COLOR` / `TERM=dumb` で無効)

## 既知の制限

- SAT solver / 複数バージョン・スロットを考慮した完全なソルバーは未実装
  (現在は決定的バックトラック。1 名 1 バージョンのレシピツリーが前提)
- binpkg リポジトリの署名と配布は未対応(index への署名が将来課題)
- 取得は http/https のみ
