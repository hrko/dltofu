package download

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/hrko/dltofu/internal/hash" // 自身のモジュールパス
	"github.com/hrko/dltofu/internal/model"
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

// FetchToFileWithHashCheck は指定されたURLからファイルをダウンロードし、
// 指定されたパスに保存すると同時に、ハッシュ値を計算して検証する。
func (d *Downloader) FetchToFileWithHashCheck(url model.ResolvedURL, destPath string, expectedHash *hash.Hash) error {
	if expectedHash == nil {
		return fmt.Errorf("expected hash is nil")
	}

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

	// ダウンロードとハッシュ計算/ファイル書き込み
	actualHash, err := d.FetchAndHash(url, expectedHash.Algorithm, tmpFile)
	if err != nil {
		return fmt.Errorf("failed to download and calculate hash: %w", err)
	}
	if !actualHash.Equal(expectedHash) {
		return fmt.Errorf("hash mismatch for %s: expected %s, got %s", url, expectedHash, actualHash)
	}
	d.logger.Debug("Hash verified successfully", "url", url, "hash", actualHash)

	// 一時ファイルを最終的なパスにリネーム (アトミック操作)
	// tmpFile を閉じる必要がある
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file %s: %w", tmpFilePath, err)
	}
	d.logger.Debug("Renaming temporary file", "from", tmpFilePath, "to", destPath)
	err = os.Rename(tmpFilePath, destPath)
	if err != nil {
		// Rename が失敗した場合、一時ファイルは残っている可能性があるが、defer での削除に任せる
		return fmt.Errorf("failed to rename temporary file %s to %s: %w", tmpFilePath, destPath, err)
	}

	d.logger.Info("File downloaded successfully", "url", url, "destination", destPath)
	return nil
}

// FetchAndHash は指定されたURLからファイルをダウンロードし、io.Writer に書き込む。
// ダウンロードと同時に、algorithm で指定されたアルゴリズムを使用してハッシュ値を計算する。
func (d *Downloader) FetchAndHash(url model.ResolvedURL, algorithm hash.HashAlgorithm, writer io.Writer) (*hash.Hash, error) {
	d.logger.Debug("Starting download and hash calculation", "url", url, "algorithm", algorithm)

	resp, err := d.open(url)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", url, err)
	}
	defer resp.Close()

	hash, err := hash.CalculateStreamTee(resp, writer, algorithm)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate hash for %s: %w", url, err)
	}

	d.logger.Debug("Downloaded and hashed successfully", "url", url, "hash", hash)
	return hash, nil
}

// Hash は指定されたURLからファイルをダウンロードし、
// 指定されたアルゴリズムでハッシュ値を計算して返す。
// ただし、ファイルは保存せず、io.Writer に書き込むこともない。
func (d *Downloader) Hash(url model.ResolvedURL, algorithm hash.HashAlgorithm) (*hash.Hash, error) {
	d.logger.Debug("Starting hash calculation", "url", url, "algorithm", algorithm)

	resp, err := d.open(url)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", url, err)
	}
	defer resp.Close()

	hash, err := hash.CalculateStream(resp, algorithm)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate hash for %s: %w", url, err)
	}

	d.logger.Debug("Hash calculated successfully", "url", url, "hash", hash)
	return hash, nil
}

// open は指定されたURLからHTTP GETリクエストを作成し、レスポンスボディを返す。
func (d *Downloader) open(url model.ResolvedURL) (io.ReadCloser, error) {
	req, err := http.NewRequest("GET", string(url), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request for %s: %w", url, err)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download from %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("failed to download from %s: received status code %d", url, resp.StatusCode)
	}

	return resp.Body, nil
}
