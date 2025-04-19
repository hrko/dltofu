package hash

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
)

const (
	AlgoSHA256 = "sha256"
	AlgoSHA512 = "sha512"
)

// GetHasher は指定されたアルゴリズムの hash.Hash を返す
func GetHasher(algorithm string) (hash.Hash, error) {
	switch strings.ToLower(algorithm) {
	case AlgoSHA256:
		return sha256.New(), nil
	case AlgoSHA512:
		return sha512.New(), nil
	default:
		return nil, fmt.Errorf("unsupported hash algorithm: %s", algorithm)
	}
}

// CalculateStream は io.Reader から読み込んでハッシュ値を計算し、16進文字列で返す
func CalculateStream(r io.Reader, algorithm string) (string, error) {
	hasher, err := GetHasher(algorithm)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(hasher, r); err != nil {
		return "", fmt.Errorf("failed to calculate hash: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// CalculateFile は指定されたファイルのハッシュ値を計算し、16進文字列で返す
func CalculateFile(filePath string, algorithm string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file %s: %w", filePath, err)
	}
	defer f.Close()
	return CalculateStream(f, algorithm)
}

// FormatHash はアルゴリズム名とハッシュ値を結合した文字列を返す (例: "sha256:...")
func FormatHash(algorithm, hashValue string) string {
	return fmt.Sprintf("%s:%s", strings.ToLower(algorithm), hashValue)
}

// ParseHash は "sha256:..." 形式の文字列からアルゴリズム名とハッシュ値を分離する
func ParseHash(formattedHash string) (algorithm string, hashValue string, err error) {
	parts := strings.SplitN(formattedHash, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid hash format: %s", formattedHash)
	}
	// アルゴリズムがサポートされているか確認しても良い
	_, err = GetHasher(parts[0])
	if err != nil {
		return "", "", fmt.Errorf("invalid hash format (unknown algorithm): %s", formattedHash)
	}
	return parts[0], parts[1], nil
}

// ValidateHash は io.Reader の内容と期待されるハッシュ値 (フォーマット済み) を比較する
func ValidateHash(r io.Reader, expectedFormattedHash string) error {
	expectedAlgo, expectedHashValue, err := ParseHash(expectedFormattedHash)
	if err != nil {
		return fmt.Errorf("invalid expected hash: %w", err)
	}

	actualHashValue, err := CalculateStream(r, expectedAlgo)
	if err != nil {
		return fmt.Errorf("failed to calculate actual hash: %w", err)
	}

	if actualHashValue != expectedHashValue {
		return fmt.Errorf("hash mismatch: expected %s, got %s", expectedFormattedHash, FormatHash(expectedAlgo, actualHashValue))
	}
	return nil
}
