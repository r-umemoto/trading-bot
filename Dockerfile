# 最新のGoイメージをベースにする
FROM golang:1.22-bullseye

# タイムゾーンを日本時間(JST)に設定（ボット開発における最重要項目）
RUN apt-get update && apt-get install -y tzdata \
    && ln -snf /usr/share/zoneinfo/Asia/Tokyo /etc/localtime \
    && echo "Asia/Tokyo" > /etc/timezone

# コンテナ内の作業ディレクトリ
WORKDIR /app

# 必要なツールがあればここに追加（今回は標準パッケージのみでOK）