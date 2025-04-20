package hash

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"strings"
)

const (
	AlgoSHA256 HashAlgorithm = "sha256"
	AlgoSHA512 HashAlgorithm = "sha512"
)

type HashAlgorithm string

type Hash struct {
	Algorithm HashAlgorithm
	HashValue []byte
}

func (h *Hash) String() string {
	return fmt.Sprintf("%s:%s", h.Algorithm, hex.EncodeToString(h.HashValue))
}

func (h *Hash) Equal(other *Hash) bool {
	if h.Algorithm != other.Algorithm {
		return false
	}
	if len(h.HashValue) != len(other.HashValue) {
		return false
	}
	for i := range h.HashValue {
		if h.HashValue[i] != other.HashValue[i] {
			return false
		}
	}
	return true
}

func (h *Hash) MarshalJSON() ([]byte, error) {
	return fmt.Appendf(nil, "\"%s\"", h.String()), nil
}

func (h *Hash) UnmarshalJSON(data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("invalid hash JSON data")
	}
	if data[0] != '"' || data[len(data)-1] != '"' {
		return fmt.Errorf("invalid hash JSON format")
	}
	formattedHash := string(data[1 : len(data)-1])
	algorithm, hashValue, err := ParseHash(formattedHash)
	if err != nil {
		return err
	}
	hashBytes, err := hex.DecodeString(hashValue)
	if err != nil {
		return fmt.Errorf("failed to decode hash value: %w", err)
	}
	h.Algorithm = algorithm
	h.HashValue = hashBytes
	return nil
}

func NewHash(algorithm HashAlgorithm, hashValue []byte) *Hash {
	return &Hash{
		Algorithm: algorithm,
		HashValue: hashValue,
	}
}

func NewHashFromString(formattedHash string) (*Hash, error) {
	algorithm, hashValue, err := ParseHash(formattedHash)
	if err != nil {
		return nil, err
	}
	hashBytes, err := hex.DecodeString(hashValue)
	if err != nil {
		return nil, fmt.Errorf("failed to decode hash value: %w", err)
	}
	return NewHash(algorithm, hashBytes), nil
}

// GetHasher は指定されたアルゴリズムの hash.Hash を返す
func GetHasher(algorithm HashAlgorithm) (hash.Hash, error) {
	switch algorithm {
	case AlgoSHA256:
		return sha256.New(), nil
	case AlgoSHA512:
		return sha512.New(), nil
	default:
		return nil, fmt.Errorf("unsupported hash algorithm: %s", algorithm)
	}
}

// CalculateStream は io.Reader から読み込んでハッシュ値を計算し、Hash 構造体を返す
func CalculateStream(r io.Reader, algorithm HashAlgorithm) (*Hash, error) {
	hasher, err := GetHasher(algorithm)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(hasher, r); err != nil {
		return nil, fmt.Errorf("failed to calculate hash: %w", err)
	}
	return &Hash{
		Algorithm: algorithm,
		HashValue: hasher.Sum(nil),
	}, nil
}

// CalculateStreamTee は io.Reader から読み込んでハッシュ値を計算し、Hash 構造体を返す。
// 同時に io.Writer にも書き込む。ファイルをダウンロードしながらハッシュ値を計算する場合などに便利。
func CalculateStreamTee(r io.Reader, w io.Writer, algorithm HashAlgorithm) (*Hash, error) {
	hasher, err := GetHasher(algorithm)
	if err != nil {
		return nil, err
	}
	multiWriter := io.MultiWriter(w, hasher)
	if _, err := io.Copy(multiWriter, r); err != nil {
		return nil, fmt.Errorf("failed to calculate hash: %w", err)
	}
	return &Hash{
		Algorithm: algorithm,
		HashValue: hasher.Sum(nil),
	}, nil
}

// ParseHash は "sha256:..." 形式の文字列からアルゴリズム名とハッシュ値を分離する
func ParseHash(formattedHash string) (algorithm HashAlgorithm, hashValue string, err error) {
	parts := strings.SplitN(formattedHash, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid hash format: %s", formattedHash)
	}
	// アルゴリズムがサポートされているか確認しても良い
	algo := HashAlgorithm(parts[0])
	hash := parts[1]
	_, err = GetHasher(algo)
	if err != nil {
		return "", "", fmt.Errorf("invalid hash format (unknown algorithm): %s", formattedHash)
	}
	return algo, hash, nil
}
