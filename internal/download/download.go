package download

import (
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/hrko/dltofu/internal/hash" // 自身のモジュールパス
)

const DefaultTimeout = 60 * time.Second

// Downloader はファイルダウンロード機能を提供
type Downloader struct {
	client *http.Client
	logger *slog.Logger
}

// NewDownloader は Downloader を作成
func NewDownloader(timeout time.Duration, logger *slog.Logger) *Downloader {
	if logger == nil {
		logger = slog.Default()
	}
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	return &Downloader{
		client: &http.Client{
			Timeout: timeout,
			// リダイレクト追従はデフォルトで有効 (最大10回)
		},
		logger: logger,
	}
}

// FetchToFile は指定されたURLからファイルをダウンロードし、指定されたパスに保存する
// expectedFormattedHash が空文字列でなければ、ダウンロード中にハッシュ検証を行う
func (d *Downloader) FetchToFile(url, destPath, expectedFormattedHash string) error {
	d.logger.Debug("Starting download", "url", url, "destination", destPath)

	// ディレクトリが存在しない場合は作成
	destDir := filepath.Dir(destPath)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory %s: %w", destDir, err)
	}

	// 一時ファイルにダウンロード
	tmpFile, err := os.CreateTemp(destDir, filepath.Base(destPath)+".*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temporary file in %s: %w", destDir, err)
	}
	tmpFilePath := tmpFile.Name()
	d.logger.Debug("Created temporary file", "path", tmpFilePath)
	// 成功・失敗に関わらず一時ファイルを閉じて削除する defer を設定
	defer func() {
		tmpFile.Close()
		// 成功時 (Rename後) は tmpFile は存在しないので Remove は失敗するが問題ない
		if _, err := os.Stat(tmpFilePath); err == nil {
			d.logger.Debug("Removing temporary file", "path", tmpFilePath)
			os.Remove(tmpFilePath)
		}
	}()

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request for %s: %w", url, err)
	}
	// 必要であれば User-Agent などを設定
	// req.Header.Set("User-Agent", "dltofu/...")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// TODO: レスポンスボディを読んで詳細なエラーメッセージを表示する？
		return fmt.Errorf("failed to download from %s: received status code %d", url, resp.StatusCode)
	}

	// ダウンロードとハッシュ計算/ファイル書き込み
	var reader io.Reader = resp.Body
	if expectedFormattedHash != "" {
		// ハッシュ検証を行う場合、TeeReader で読みながらハッシュ計算とファイル書き込み
		expectedAlgo, _, err := hash.ParseHash(expectedFormattedHash)
		if err != nil {
			return fmt.Errorf("invalid expected hash format '%s': %w", expectedFormattedHash, err)
		}
		hasher, _ := hash.GetHasher(expectedAlgo) // エラーチェックは ParseHash で済んでいる想定
		reader = io.TeeReader(resp.Body, hasher)

		// ストリームをファイルに書き込む
		_, err = io.Copy(tmpFile, reader)
		if err != nil {
			return fmt.Errorf("failed to write downloaded content to %s: %w", tmpFilePath, err)
		}
		// 書き込みが終わってからハッシュを比較
		actualHash := hex.EncodeToString(hasher.Sum(nil))
		actualFormattedHash := hash.FormatHash(expectedAlgo, actualHash)
		if actualFormattedHash != expectedFormattedHash {
			return fmt.Errorf("hash mismatch for %s: expected %s, got %s", url, expectedFormattedHash, actualFormattedHash)
		}
		d.logger.Debug("Hash verified successfully", "url", url, "hash", actualFormattedHash)

	} else {
		// ハッシュ検証なしの場合、そのままファイルに書き込む (lock コマンド用)
		_, err = io.Copy(tmpFile, reader)
		if err != nil {
			return fmt.Errorf("failed to write downloaded content to %s: %w", tmpFilePath, err)
		}
		d.logger.Debug("Downloaded file without hash verification", "url", url)
	}

	// 一時ファイルを最終的なパスにリネーム (アトミック操作)
	// tmpFile を閉じる必要がある
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file %s: %w", tmpFilePath, err)
	}

	d.logger.Debug("Renaming temporary file", "from", tmpFilePath, "to", destPath)
	err = os.Rename(tmpFilePath, destPath)
	if err != nil {
		// Rename が失敗した場合、一時ファイルは残っている可能性がある
		// defer での削除に任せる
		return fmt.Errorf("failed to rename temporary file %s to %s: %w", tmpFilePath, destPath, err)
	}

	d.logger.Info("File downloaded successfully", "url", url, "destination", destPath)
	return nil
}

// FetchToTemp は URL からダウンロードし、一時ファイルに保存してそのパスを返す
// 主に lock コマンドでハッシュ計算のために一時的にダウンロードする際に使用
func (d *Downloader) FetchToTemp(url string) (string, error) {
	d.logger.Debug("Starting temporary download", "url", url)

	// 一時ファイルを作成 (システム標準の一時ディレクトリを使用)
	tmpFile, err := os.CreateTemp("", "dltofu-*.tmp")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary file: %w", err)
	}
	tmpPath := tmpFile.Name()
	// ここでは defer で削除しない。呼び出し元が責任を持つ。
	// (ハッシュ計算が終わったら削除するため)
	d.logger.Debug("Created temporary file for hashing", "path", tmpPath)
	defer tmpFile.Close() // Close は必須

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		_ = os.Remove(tmpPath) // エラー時は一時ファイルを削除
		return "", fmt.Errorf("failed to create request for %s: %w", url, err)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to download from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to download from %s: received status code %d", url, resp.StatusCode)
	}

	// ファイルに書き込み
	_, err = io.Copy(tmpFile, resp.Body)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to write downloaded content to %s: %w", tmpPath, err)
	}

	d.logger.Debug("File downloaded to temporary location", "url", url, "path", tmpPath)
	return tmpPath, nil // 一時ファイルのパスを返す
}
