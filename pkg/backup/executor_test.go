package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExecutor_parseBackupMetadata(t *testing.T) {
	tests := []struct {
		name             string
		logs             string
		expectedSize     int64
		expectedChecksum string
	}{
		{
			name:             "valid logs",
			logs:             "Backup Size: 12345\nBackup Checksum: abcde12345",
			expectedSize:     12345,
			expectedChecksum: "abcde12345",
		},
		{
			name:             "invalid logs",
			logs:             "some other logs",
			expectedSize:     0,
			expectedChecksum: "",
		},
		{
			name:             "empty logs",
			logs:             "",
			expectedSize:     0,
			expectedChecksum: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &Executor{}
			size, checksum := executor.parseBackupMetadata(tt.logs)
			assert.Equal(t, tt.expectedSize, size)
			assert.Equal(t, tt.expectedChecksum, checksum)
		})
	}
}
