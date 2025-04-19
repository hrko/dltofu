package lock

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

const LockFileName = "dltofu.lock"
const LockFileVersion = 1

// LockFile は dltofu.lock ファイルの内容を表す
type LockFile struct {
	Version       int                          `json:"version"`
	HashAlgorithm string                       `json:"hash_algorithm"` // このLock生成時のグローバルデフォルト
	Files         map[string]map[string]string `json:"files"`          // key1: file_id, key2: resolved_url, value: formatted_hash

	path   string       // Lockファイルのパス
	mu     sync.RWMutex // Files マップへのアクセスを保護
	logger *slog.Logger
}

// NewLockFile は空の LockFile 構造体を作成する
func NewLockFile(configHashAlgo string, logger *slog.Logger) *LockFile {
	if logger == nil {
		logger = slog.Default()
	}
	return &LockFile{
		Version:       LockFileVersion,
		HashAlgorithm: configHashAlgo, // 設定ファイルのデフォルトを記録
		Files:         make(map[string]map[string]string),
		logger:        logger,
	}
}

// LoadLockFile は指定されたディレクトリから dltofu.lock を読み込む
func LoadLockFile(dirPath string, logger *slog.Logger) (*LockFile, error) {
	if logger == nil {
		logger = slog.Default()
	}
	lockPath := filepath.Join(dirPath, LockFileName)
	logger.Debug("Attempting to load lock file", "path", lockPath)

	data, err := os.ReadFile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Info("Lock file not found", "path", lockPath)
			// ファイルが存在しない場合はエラーではなく、空の LockFile を返すか、
			// 呼び出し元でハンドリングするか -> download コマンドではエラーにする必要あり
			return nil, fmt.Errorf("lock file not found at %s: %w", lockPath, err) // download コマンドのためにエラーを返す
		}
		return nil, fmt.Errorf("failed to read lock file %s: %w", lockPath, err)
	}

	var lf LockFile
	err = json.Unmarshal(data, &lf)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal lock file %s: %w", lockPath, err)
	}

	if lf.Version != LockFileVersion {
		return nil, fmt.Errorf("unsupported lock file version: %d (supported: %d)", lf.Version, LockFileVersion)
	}

	if lf.Files == nil {
		// 空のファイルでも files フィールドは存在すべき
		lf.Files = make(map[string]map[string]string)
	}

	lf.path = lockPath // パスを記憶
	lf.logger = logger
	logger.Info("Lock file loaded successfully", "path", lockPath)
	return &lf, nil
}

// Save は現在の LockFile の内容をファイルに書き込む
func (lf *LockFile) Save(dirPath string) error {
	lf.mu.Lock() // 書き込み中はロック
	defer lf.mu.Unlock()

	if lf.path == "" { // 新規作成の場合
		lf.path = filepath.Join(dirPath, LockFileName)
	}

	lf.logger.Debug("Saving lock file", "path", lf.path)
	data, err := json.MarshalIndent(lf, "", "  ") // 整形して出力
	if err != nil {
		return fmt.Errorf("failed to marshal lock file data: %w", err)
	}

	// ファイルに書き込む (アトミックな書き込みを考慮すると、一時ファイル経由が良い)
	tmpPath := lf.path + ".tmp"
	err = os.WriteFile(tmpPath, data, 0644)
	if err != nil {
		return fmt.Errorf("failed to write temporary lock file %s: %w", tmpPath, err)
	}

	// 一時ファイルをリネームしてアトミックに置き換え
	err = os.Rename(tmpPath, lf.path)
	if err != nil {
		// リネーム失敗した場合、一時ファイルを削除する試み
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temporary lock file to %s: %w", lf.path, err)
	}

	lf.logger.Info("Lock file saved successfully", "path", lf.path)
	return nil
}

// GetHash は指定されたファイルIDと解決済みURLに対応するハッシュ値を取得する
func (lf *LockFile) GetHash(fileID, resolvedURL string) (string, bool) {
	lf.mu.RLock() // 読み取りロック
	defer lf.mu.RUnlock()

	if fileLocks, ok := lf.Files[fileID]; ok {
		hashVal, found := fileLocks[resolvedURL]
		return hashVal, found
	}
	return "", false
}

// SetHash はハッシュ値を設定する。既存の値があり、新しい値と異なる場合はエラーを返す。
func (lf *LockFile) SetHash(fileID, resolvedURL, newFormattedHash string) error {
	lf.mu.Lock() // 書き込みロック
	defer lf.mu.Unlock()

	if lf.Files[fileID] == nil {
		lf.Files[fileID] = make(map[string]string)
	}

	existingHash, found := lf.Files[fileID][resolvedURL]
	if found && existingHash != newFormattedHash {
		// TOFU: 初回以降でハッシュが変わったらエラー
		return fmt.Errorf("hash inconsistency for %s [%s]: existing '%s', new '%s'",
			fileID, resolvedURL, existingHash, newFormattedHash)
	}

	// 新規またはハッシュが同じ場合は設定/上書き
	lf.Files[fileID][resolvedURL] = newFormattedHash
	return nil
}

// RemoveEntry は指定されたファイルIDのエントリ全体を削除する
func (lf *LockFile) RemoveEntry(fileID string) {
	lf.mu.Lock()
	defer lf.mu.Unlock()
	delete(lf.Files, fileID)
}

// RemoveURL は特定のURLエントリを削除する
func (lf *LockFile) RemoveURL(fileID, resolvedURL string) {
	lf.mu.Lock()
	defer lf.mu.Unlock()
	if fileLocks, ok := lf.Files[fileID]; ok {
		delete(fileLocks, resolvedURL)
		// fileID のマップが空になったら fileID 自体も削除する？ -> しても良いが見やすさのため残す
		// if len(fileLocks) == 0 {
		//     delete(lf.Files, fileID)
		// }
	}
}

// Prune は設定ファイルに存在するファイルIDとURLのみをLockファイルに残し、他を削除する
// activeFiles: map[fileID]map[resolvedURL]struct{}
func (lf *LockFile) Prune(activeFiles map[string]map[string]struct{}) {
	lf.mu.Lock()
	defer lf.mu.Unlock()

	prunedFiles := make(map[string]map[string]string)

	for fileID, activeURLs := range activeFiles {
		if existingURLs, ok := lf.Files[fileID]; ok {
			prunedURLs := make(map[string]string)
			for url, hashVal := range existingURLs {
				if _, isActive := activeURLs[url]; isActive {
					prunedURLs[url] = hashVal // アクティブなURLのみ保持
				} else {
					lf.logger.Debug("Pruning inactive URL from lock file", "file_id", fileID, "url", url)
				}
			}
			if len(prunedURLs) > 0 {
				prunedFiles[fileID] = prunedURLs
			} else {
				lf.logger.Debug("Pruning inactive file entry from lock file", "file_id", fileID)
			}
		}
	}
	lf.Files = prunedFiles // Prune 後のマップで置き換える
}
