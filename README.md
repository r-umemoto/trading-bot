# Trading Bot

## 実行手順

1. 以下のコマンドで実行ファイルをビルドします。
   ```bash
   env GOOS=windows GOARCH=amd64 go build -o bot.exe ./cmd/bot
   ```

2. 実行ファイルと同じディレクトリに`.env`ファイルを配置します。

   `.env`ファイルには以下の環境変数を設定します。

   ```
   BROKER_TYPE=kabu
   KABU_API_URL=http://localhost:18081/kabusapi
   KABU_PASSWORD=<あなたのパスワード>
   ```

   - `BROKER_TYPE`: `kabu` を設定します（株ステーション用）。
   - `KABU_API_URL`: 検証用は `http://localhost:18081/kabusapi`、本番用は `http://localhost:18080/kabusapi` を設定します。
   - `KABU_PASSWORD`: 株ステーションのパスワードを設定します。

3. PowerShellから実行ファイルを実行します（実行ファイルと同じディレクトリにいることを確認してください）。
   ```powershell
   ./bot.exe
   ```
