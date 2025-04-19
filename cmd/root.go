package cmd

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
)

var (
	cfgFile  string // 設定ファイルパスを保持する変数
	logLevel string // ログレベル指定用
	logger   *slog.Logger
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "dltofu",
	Short: "A tool to download files securely using hash verification (TOFU model)",
	Long: `dltofu helps manage downloading external binaries or archives for CI/CD
or development environments. It verifies downloads against a lock file
containing pre-calculated hashes.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// ロガーの初期化
		var lvl slog.Level
		switch logLevel {
		case "debug":
			lvl = slog.LevelDebug
		case "info":
			lvl = slog.LevelInfo
		case "warn":
			lvl = slog.LevelWarn
		case "error":
			lvl = slog.LevelError
		default:
			lvl = slog.LevelInfo // デフォルトは Info
		}
		opts := &slog.HandlerOptions{Level: lvl}
		handler := slog.NewTextHandler(os.Stderr, opts)
		logger = slog.New(handler)
		slog.SetDefault(logger) // 標準の slog 出力も設定

		// 設定ファイルパスの解決 (デフォルト値)
		if cfgFile == "" {
			// カレントディレクトリの dltofu.yml or dltofu.yaml を探す
			if _, err := os.Stat("dltofu.yml"); err == nil {
				cfgFile = "dltofu.yml"
			} else if _, err := os.Stat("dltofu.yaml"); err == nil {
				cfgFile = "dltofu.yaml"
			}
			// 見つからない場合、後続処理でエラーにするか、ここでは何もしないか
			// lock, download コマンドでは必須なので、そちらでチェックする
		}
		logger.Debug("Using configuration file", "path", cfgFile)

		return nil
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		// Cobra は通常エラーを出力するが、念のため
		// logger は PersistentPreRunE で初期化されるため、Execute のエラー時には使えない可能性がある
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	// グローバルなフラグを追加
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file (default is dltofu.yml or dltofu.yaml in current directory)")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "Set log level (debug, info, warn, error)")
}
