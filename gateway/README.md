# えっち感知器 — Netlify AI Gateway中継サーバー

GCEなどで動くDiscord Bot「えっち感知器」からNetlify AI Gateway経由でTypeSafe Jevを呼び出すための、認証付きNetlify Functionです。Bot本体は隣の `../EcchiDetector` にあります。このフォルダだけでNetlifyへ配置できます。

```text
EcchiDetector（GCE）
  → Netlify Function（共有シークレットで認証）
  → Netlify AI Gateway
  → TypeSafe Jev
```

## 構成

- `netlify/functions/jev.mjs`: `/api/jev` の中継Function
- `netlify/jev.test.mjs`: 認証・入力検証・Gateway呼び出し・応答変換のHTTPモックテスト
- `netlify/public/relay.txt`: 最小の静的配置ファイル
- `netlify.toml`: Functionsと公開ディレクトリの設定

Node.js 22を使用します。標準の `fetch` と暗号APIだけで動き、npm依存はありません。

## Netlifyへの配置

1. Netlifyで中継サーバー用のサイトを作り、クレジット制プランのAI Gatewayを利用できる状態にします。
2. サイト設定で、Functionsスコープの `AI_PROVIDER_API_KEY` に十分長いランダムな共有シークレットを設定します。Bot側にも同じ値を設定します。
3. `TYPESAFE_API_KEY` / `TYPESAFE_BASE_URL` は手動設定せず、Netlifyの自動注入を使います。手動設定済みの場合は削除してください。これらをGCEへコピーする必要はありません。
4. このフォルダをプロジェクトルートとして、作成したサイトにリンクして本番デプロイします。

PowerShellで実行:

```powershell
Set-Location C:\ogatomo\codex_temp\EcchiDetector\gateway
npx netlify-cli login
npx netlify-cli link
npx netlify-cli deploy --prod
```

Git連携で配置する場合も、このフォルダをNetlifyのBase directoryにします。Gitリポジトリのルートがこのフォルダなら、Base directoryの変更は不要です。

[Netlify公式ドキュメント](https://docs.netlify.com/build/ai-gateway/overview/)に従い、Gatewayの認証情報はFunction内で注入された値を使用します。手動設定したキー・URLは自動注入より優先されます。AI Gatewayの有効化には、一度以上の本番デプロイが必要です。

共有シークレットの生成例:

```powershell
node -e "console.log(require('node:crypto').randomBytes(32).toString('hex'))"
```

Netlifyの個人アクセストークンやGatewayのキーは、この共有シークレットとして使いません。漏えい時はNetlifyとBotの両側で共有シークレットを交換してください。

## Bot側の設定

```dotenv
AI_PROVIDER=netlify
AI_PROVIDER_API_KEY=Functionと同じ共有シークレット
NETLIFY_FUNCTION_URL=https://example.netlify.app/api/jev
```

Botは指定URLへそのままPOSTします。`NETLIFY_FUNCTION_URL` に `/v1/systemone` は付けません。

## API

`POST /api/jev`、`Authorization: Bearer <共有シークレット>`、`Content-Type: application/json` を使用します。Botが生成するTypeSafe形式の判定リクエストを受け付けます。

- JSON本文は32 KiBまで、判定対象の本文は4000文字まで。
- モデルは `jev-latest`、判定項目は `sexual_suggestiveness` の `noul` 型1件のみ。
- 判定プロンプトはBot側で管理し、Functionは必要なフィールドだけをGatewayへ送信。
- Gatewayへのリクエストは15秒でタイムアウトし、リダイレクトに追従しない。
- 本文・キー・Gatewayのエラー本文はFunctionのログやレスポンスへ出さない。

成功時は、Bot既存の型で読み取れるスコアだけを返します。

```json
{"answers":{"sexual_suggestiveness":{"noul":0.42}}}
```

レート制限とSQLiteキャッシュはBot側で管理します。共有シークレットを持つ呼び出し元を信頼する構成です。利用料はGatewayとFunctionの利用に応じてNetlifyのクレジットから消費され、Freeプランでも無制限には利用できません。

## テスト

このフォルダで実行します。

```powershell
node --test netlify/jev.test.mjs
```

テストはHTTPモックを使用し、Gatewayへ実際のリクエストは送りません。デプロイ先のGateway認証は別途確認が必要です。GitHub Actionsでも同じテストを実行します。

## ライセンス

MIT License
