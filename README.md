# zpkg

ZIGNOS のソースベースパッケージマネージャ(Go 単一バイナリ)。KISS Linux の `kiss` を参考に最小構成から始め、将来的に Portage / Paludis 相当へ進化させる。

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

./zpkg install dl-test       # 実ダウンロード + sha256 検証 + 展開の経路テスト(busybox 1.36.1 tarball)
./zpkg remove dl-test
./zpkg install hello && ./zpkg install hello-conflict   # → "bin/hello: already owned by hello" で失敗することを確認
./zpkg remove hello
```

注意: Windows の開発ホストでは `ZPKG_REPOS` の区切りは `;` を使う(例: `D:/path/to/testdata/repos/core`)。Unix は `:`。

## コマンド

| コマンド | 動作 |
|---|---|
| `zpkg depends <pkg>...` | 依存を含むビルド順の表示 |
| `zpkg build <pkg>...` | fetch → 検証 → ビルド(destroot まで。インストールはしない) |
| `zpkg install <pkg>...` | 必要ならビルドしてから `$ZPKG_ROOT` へインストール |
| `zpkg remove <pkg>...` | インストール済みパッケージの削除 |
| `zpkg list` | インストール済み一覧 |
| `zpkg search <term>` | レシピ検索 |

## 環境変数

| 変数 | 既定 | 内容 |
|---|---|---|
| `ZPKG_REPOS` | なし(必須) | レシプリポジトリ。OS のパス区切りで複数指定(Unix は `:`、Windows は `;`)。先頭一致検索・先頭が優先(overlay) |
| `ZPKG_ROOT` | `/` | ターゲットルート |
| `ZPKG_CACHE` | `$ZPKG_ROOT/var/cache/zpkg` | ソースキャッシュ・ビルド作業領域 |
| `ZPKG_DB` | `$ZPKG_ROOT/var/lib/zpkg` | インストール済み DB |
| `ZPKG_FORCE` | なし | `1` で改変済みパッケージの remove を許可 |

## レシピ形式(ディレクトリ型・KISS 式)

```
<repo>/<pkg>/
├── version     # 1 行。必須
├── sources     # 1 行 1 URL。省略可
├── checksums   # 1 行 1 sha256。sources と同数・同順
├── depends     # 1 行 1 パッケージ名。省略可
└── build       # shell スクリプト。必須
```

空行と `#` コメントは無視。`build` は `bash` で実行され、`$ZPKG_DESTROOT` に成果物を置く契約(`make DESTDIR="$ZPKG_DESTROOT" install` 等)。

## 既知の制限(v0)

- パスに空白を含むファイルは扱えない(マニフェスト形式が空白区切り)
- バージョン制約なしの依存解決(名前のみ・トポロジカルソート)
- リポジトリ署名なし、バイナリキャッシュなし
- 取得は http/https のみ
