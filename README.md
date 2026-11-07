# えっち感知器 (EcchiDetector)

DiscordのメッセージをJevに判定させるDiscord Botです。判定先はTypeSafe API直結、Vercel AI Gateway、Netlify AI Gatewayから選べます。VercelではTypeSafe AIだけを許可し、Netlifyでは認証付きNetlify Functionを中継してJevを呼びます。

設定しきい値以上のスコアに対する返信確率は、`0.10〜0.19: 10%`、`0.20〜0.29: 20%`、以降0.10刻みで10%ずつ上がり、`0.90〜0.99: 90%`、`1.00: 100%` です。しきい値の初期値は `0.20` なので、`0.10〜0.19` の帯も使う場合は `/ecchi threshold value:0.10` に設定します。

返信時は6種類の文面からランダムに選び、文末に判定スコアを整数％で表示します。絵文字返信では絵文字だけを返信し、その後にえっち度を別メッセージで送ります。返信元には `👀` のリアクションも付けます。同じユーザーへの返信は全サーバー合算で30秒に1回までです。

Jevの判定結果は確率的であり、誤検出・見逃しがあります。しきい値以上でも、設定スコア帯の返信確率で抽選されます。

Jevにはメッセージ本文を送ります。キャッシュキーの生成時だけDiscordメンション部分を除去し、空白を整理して連続する同一の句読点を1文字に圧縮します。`+` などの記号や絵文字は残すため、意味の異なる本文を同じキャッシュ扱いしにくくしています。同じ正規化後の文章はSQLiteからスコアを再利用します。キャッシュには本文ではなく、モデル・判定プロンプト・正規化後本文から作ったSHA-256キーとスコアを保存します。短い定型文のハッシュは推測される可能性があります。

## 必要な設定

- Go 1.26以降
- Discord Bot Token (`DISCORD_TOKEN`)
- `AI_PROVIDER`: `vercel`、`typesafe`、`netlify`（省略時は `vercel`）
- `AI_PROVIDER_API_KEY`: Vercel / TypeSafeのAPIキー、またはNetlify Functionと共有するシークレット。全プロバイダー共通の設定名です
- `NETLIFY_FUNCTION_URL`: 認証付きFunctionの完全なURL（`AI_PROVIDER=netlify` の場合のみ必須）

以前の `AI_GATEWAY_API_KEY` / `TYPESAFE_API_KEY` はBotの設定として読み取りません。既存環境では、使用中のキーの値を `AI_PROVIDER_API_KEY` に設定してから新しいバイナリへ更新してください。

Botは `Guilds`、`Guild Messages`、`Message Content` intentsを使います。Discord Developer PortalのPrivileged Gateway Intentsで `Message Content Intent` を有効にしてください。DM intentは使いません。OAuth2の招待URLには `bot` と `applications.commands` スコープを選び、Botには対象チャンネルの `View Channel`、`Send Messages`、`Add Reactions` 権限を付与します。スレッド内で使う場合は `Send Messages in Threads` も必要です。

サーバー参加時は、Jevによる判定とメッセージ本文の送信先を案内します。`/ecchi threshold`、`/ecchi ignore`、`/ecchi allow`、`/ecchi settings` はサーバー管理権限を持つ人が使えます。設定はサーバーごとに保存されます。

## データとプライバシー

- Botはサーバーのユーザーメッセージを監視します。DMとBotのメッセージは判定しません。自動判定では除外チャンネルを処理しませんが、ユーザーがメッセージの「えっち度を見る」を明示的に実行した場合は、そのメッセージを判定します。
- 判定対象の本文は選択した判定先（TypeSafe API直結、Vercel AI Gateway経由、またはNetlify Function → Netlify AI Gateway経由でTypeSafe AI）へ送信されます。サーバーへ導入する前に、サーバーの利用者へこの送信を周知してください。
- Botは本文をSQLiteやログに保存しません。キャッシュには本文の正規化ハッシュと判定スコアを保存します。短い定型文のハッシュは推測される可能性があります。
- SQLiteキャッシュは期限なく保存されます。消去する場合はBotを停止し、`classification-cache.db` を削除してください。
- `data/config.json` にはサーバーID、除外チャンネルID、しきい値などを保存します。ローカルでは `.gitignore` により `data/` はGit対象外です。
- `--debug` はメッセージIDと判定値をログへ出します。本文は記録しません。

## レート制限と費用

ユーザーごとに全サーバー合算で直近60秒に最大20件、サーバーごとに最大60件まで判定します。キャッシュヒットも件数に含み、制限超過分は判定しません。制限カウンターはメモリ上にあり、Botの再起動でリセットされます。複数インスタンスで動かす場合はインスタンスごとに上限が適用されます。

この制限だけでは利用料金の上限になりません。選択したサービス側で利用状況を監視し、必要な予算・通知を設定してください。

## ローカル実行

PowerShellでプロジェクト直下を開いて実行します。

```powershell
$env:DISCORD_TOKEN = "Discord Bot Token"
$env:AI_PROVIDER = "vercel" # または "typesafe" / "netlify"
$env:AI_PROVIDER_API_KEY = "選択したサービスのAPIキー"
go run .
```

Netlify Functionを経由する場合:

```powershell
$env:AI_PROVIDER = "netlify"
$env:AI_PROVIDER_API_KEY = "BotとFunctionで共有するランダムなシークレット"
$env:NETLIFY_FUNCTION_URL = "https://example.netlify.app/api/jev"
go run . --debug
```

`NETLIFY_FUNCTION_URL` はHTTPSを使用し、クエリ、認証情報を含めないでください。Go側はこのURLへそのままPOSTし、`/v1/systemone` を追加しません。認証情報のリダイレクト送信を避けるため、Functionのリダイレクトは追従しません。キーやURLの未設定時に他のプロバイダーへ切り替えることはありません。Netlifyのキャッシュは他のプロバイダーと分離します。

## Netlify Functionの配置

接続経路は `GCEのBot → 認証付きFunction → Netlify AI Gateway → TypeSafe Jev` です。[Netlify公式ドキュメント](https://docs.netlify.com/build/ai-gateway/overview/)に従い、Gatewayの `TYPESAFE_API_KEY` / `TYPESAFE_BASE_URL` はFunction内で自動注入された値を使います。これらをGCEへコピーする必要はありません。

中継サーバーは [gateway](gateway/) にあります。[中継サーバーのREADME](gateway/README.md)に従い、`gateway` フォルダをNetlifyのプロジェクトルートとして配置してください。Netlifyのデプロイコマンドはこのサブフォルダで実行してください。

Bot側にはFunctionのURLと共有シークレットを設定します。レート制限とSQLiteキャッシュはBot側で管理します。Bot側のテストは `go test ./...` で実行できます。

## Botの操作

判定スコアをメッセージごとにログへ出す場合:

```powershell
go run . --debug
```

ビルド済みの場合は `EcchiDetector.exe --debug` で起動できます。debugログにはメッセージIDとスコアのみを出力し、本文は出しません。

スラッシュコマンド:

- `/ecchi threshold value:0.25` — このサーバーのしきい値を変更（0.0〜1.0）
- `/ecchi ignore channel:#雑談` — チャンネルを検知対象から除外
- `/ecchi allow channel:#雑談` — 除外を解除
- `/ecchi settings` — 現在のしきい値と除外チャンネルを表示

メッセージ右クリック（スマートフォンでは長押し）→「アプリ」→「えっち度を見る」で、そのメッセージを個別判定できます。結果は実行した本人だけに表示されます。自動返信のしきい値・返信確率・返信クールダウンは適用せず、通常メッセージと同じレート制限（ユーザー20件/分、サーバー60件/分）を共有します。

ローカルでは `data/config.json`、systemdでは `/var/lib/ecchidetector/config.json` に保存します。`CONFIG_PATH` 環境変数で保存先を変更できます。

判定キャッシュはローカルでは `data/classification-cache.db`、systemdでは `/var/lib/ecchidetector/classification-cache.db` に保存します。`CACHE_PATH` 環境変数で変更できます。キャッシュを消去するときはBotを停止してからこのDBファイルを削除してください。モデルまたは判定プロンプトを変えるとキャッシュキーの版が変わり、古い結果は再利用されません。

選択した判定先の利用料金が発生します。認証情報はソースコードや公開リポジトリに保存しないでください。

## ビルド

```powershell
go build -o EcchiDetector.exe .
```

Linux amd64向け:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o EcchiDetector .
```

## GCE / Debianへの配置

Linux向けの `EcchiDetector` と `EcchiDetector.service` をサーバーへコピーしてから実行します。

```bash
sudo install -d -m 0755 /opt/EcchiDetector
sudo install -m 0755 EcchiDetector /opt/EcchiDetector/EcchiDetector
sudo install -m 0644 EcchiDetector.service /etc/systemd/system/EcchiDetector.service
sudo useradd --system --user-group --home-dir /opt/EcchiDetector --shell /usr/sbin/nologin ecchidetector
sudo install -m 0600 /dev/null /etc/ecchidetector.env
sudoedit /etc/ecchidetector.env
```

既存の `jevdetector` unit から移行する場合は、新unitを有効化する前に停止・無効化します。

```bash
sudo systemctl disable --now jevdetector.service
```

`/etc/ecchidetector.env`:

```ini
DISCORD_TOKEN=Discord Bot Token
AI_PROVIDER=vercel
AI_PROVIDER_API_KEY=Vercel AI Gateway API Key
# TypeSafe直結ではAI_PROVIDER_API_KEYをTypeSafeのキーに変更します
# AI_PROVIDER=typesafe
# NetlifyではAI_PROVIDER_API_KEYをFunctionとの共有シークレットに変更します
# AI_PROVIDER=netlify
# NETLIFY_FUNCTION_URL=https://example.netlify.app/api/jev
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now EcchiDetector.service
```

systemdの `StateDirectory=ecchidetector` が `/var/lib/ecchidetector` を作成し、設定ファイルの保存領域として使います。

操作:

```bash
sudo systemctl status EcchiDetector.service
sudo systemctl restart EcchiDetector.service
sudo systemctl stop EcchiDetector.service
sudo journalctl -u EcchiDetector.service -f
```

## ライセンス

MIT License
