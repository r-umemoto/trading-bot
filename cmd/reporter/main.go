package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/r-umemoto/trading-bot/pkg/domain/report"
	reportinfra "github.com/r-umemoto/trading-bot/pkg/infra/report"
)

func main() {
	var (
		dateStr   = flag.String("date", "", "アップロード対象の日付 (形式: YYYY-MM-DD, 省略時は本日JST)")
		filePath  = flag.String("file", "", "アップロードする単一のレポートJSONファイルパス (指定時は-dateより優先)")
		dirPath   = flag.String("dir", "./data/reports", "レポートJSONファイルが格納されているディレクトリ")
		projectID = flag.String("project", "", "GCPプロジェクトID (省略時はGCP_PROJECT_ID環境変数またはデフォルト値)")
		dryRun    = flag.Bool("dry-run", false, "Firestoreへの送信を行わず、読み込んだ内容の確認のみ実行")
		verbose   = flag.Bool("verbose", false, "詳細ログを出力")
	)
	flag.Parse()

	// ログ設定
	logLevel := slog.LevelInfo
	if *verbose {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	// 対象日付の決定 (未指定ならJSTの本日)
	targetDate := *dateStr
	if targetDate == "" && *filePath == "" {
		jst, err := time.LoadLocation("Asia/Tokyo")
		if err != nil {
			targetDate = time.Now().Format("2006-01-02")
		} else {
			targetDate = time.Now().In(jst).Format("2006-01-02")
		}
	}

	// 読み込み対象ファイルの特定
	targetFilePath := *filePath
	if targetFilePath == "" {
		targetFilePath = filepath.Join(*dirPath, "daily_"+targetDate+".json")
	}

	slog.Info("🔍 レポートファイルの読み込みを開始します", slog.String("file", targetFilePath))

	dailyReport, err := reportinfra.LoadFromFile(targetFilePath)
	if err != nil {
		slog.Error("❌ レポートファイルの読み込みに失敗しました", slog.String("file", targetFilePath), slog.Any("error", err))
		os.Exit(1)
	}

	// レポートの基本バリデーション
	if dailyReport.Date == "" {
		slog.Error("❌ レポートデータの日付が空です")
		os.Exit(1)
	}

	// 読み込んだレポートの概要表示
	printReportSummary(dailyReport)

	// DryRunモードの場合は送信せず終了
	if *dryRun {
		slog.Info("⚠️ [DRY-RUN] Firestoreへのアップロードはスキップされました")
		return
	}

	// GCP Project ID & 認証情報（GOOGLE_APPLICATION_CREDENTIALS）の自動解決
	creds, pid := resolveCredentialsAndProject(*projectID)

	ctx := context.Background()

	// 認証情報の注意喚起ログ
	if creds == "" {
		slog.Warn("⚠️ GOOGLE_APPLICATION_CREDENTIALS が設定されていません。Application Default Credentials (ADC) を試行します")
	} else {
		slog.Info("🔑 認証情報ファイルを使用します", slog.String("credentials", creds))
	}

	slog.Info("🚀 Firestore クライアントを初期化します", slog.String("projectID", pid))
	client, err := firestore.NewClient(ctx, pid)
	if err != nil {
		slog.Error("❌ Firestoreクライアントの作成に失敗しました", slog.Any("error", err))
		os.Exit(1)
	}
	defer client.Close()

	repo := reportinfra.NewFirestoreRepository(client)
	if err := repo.Save(ctx, dailyReport); err != nil {
		slog.Error("❌ Firestoreへのレポート保存に失敗しました", slog.String("date", dailyReport.Date), slog.Any("error", err))
		os.Exit(1)
	}

	slog.Info("✅ Firestoreへの日次レポートアップロードが正常に完了しました！",
		slog.String("collection", "daily_reports"),
		slog.String("doc", dailyReport.Date),
	)
}

// resolveCredentialsAndProject は .env や同ディレクトリのサービスアカウントキーから自動解決します
func resolveCredentialsAndProject(customProject string) (string, string) {
	searchDirs := []string{"."}
	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		if exeDir != "." {
			searchDirs = append(searchDirs, exeDir)
		}
	}

	// 1. .env を探索して環境変数に読み込み
	for _, dir := range searchDirs {
		envPath := filepath.Join(dir, ".env")
		loadDotEnv(envPath)
	}

	// 2. GOOGLE_APPLICATION_CREDENTIALS の解決
	creds := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if creds == "" || !fileExists(creds) {
		for _, dir := range searchDirs {
			candidates, _ := filepath.Glob(filepath.Join(dir, "daytrade-agent-*.json"))
			if len(candidates) == 0 {
				candidates, _ = filepath.Glob(filepath.Join(dir, "*credential*.json"))
			}
			if len(candidates) > 0 {
				creds = candidates[0]
				os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", creds)
				break
			}
		}
	}

	// 3. プロジェクトIDの解決
	pid := customProject
	if pid == "" {
		pid = os.Getenv("GCP_PROJECT_ID")
	}
	if pid == "" && creds != "" && fileExists(creds) {
		if p, err := extractProjectID(creds); err == nil && p != "" {
			pid = p
		}
	}
	if pid == "" {
		pid = "trading-bot-project"
	}

	return creds, pid
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func loadDotEnv(p string) {
	f, err := os.Open(p)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		k := strings.TrimSpace(parts[0])
		v := strings.TrimSpace(parts[1])
		if k != "" && os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

func extractProjectID(jsonPath string) (string, error) {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return "", err
	}
	var sa struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(data, &sa); err != nil {
		return "", err
	}
	return sa.ProjectID, nil
}

func printReportSummary(r *report.DailyReport) {
	fmt.Println()
	fmt.Println("================ [日次レポート概要] ================")
	fmt.Printf(" 日付       : %s\n", r.Date)
	fmt.Printf(" 更新日時   : %s\n", r.UpdatedAt.Format(time.RFC3339))
	fmt.Printf(" 総取引回数 : %d 回 (勝: %d, 負: %d, 分: %d)\n",
		r.Total.Trades, r.Total.Wins, r.Total.Losses, r.Total.Draws)
	fmt.Printf(" 勝率       : %.1f %%\n", r.Total.WinRate)
	fmt.Printf(" 実現損益   : %.1f 円\n", r.Total.RealizedPnL)
	fmt.Printf(" 含み損益   : %.1f 円\n", r.Total.UnrealizedPnL)
	fmt.Printf(" 合計損益   : %.1f 円\n", r.Total.TotalPnL)
	fmt.Printf(" 銘柄数     : %d 件\n", len(r.Symbols))
	fmt.Printf(" 戦略数     : %d 件\n", len(r.Strats))
	fmt.Println("===================================================")
	fmt.Println()
}
