package db

import (
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/pkg/errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func GetS3ObjectMeta(path string) (*model.S3ObjectMeta, error) {
	var m model.S3ObjectMeta
	err := db.Where("path_hash = ?", model.HashS3ObjectPath(path)).Take(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return &m, nil
}

// GetS3ObjectMetas returns the records for the given paths, keyed by path.
func GetS3ObjectMetas(paths []string) (map[string]*model.S3ObjectMeta, error) {
	res := make(map[string]*model.S3ObjectMeta, len(paths))
	const batch = 500
	for start := 0; start < len(paths); start += batch {
		end := min(start+batch, len(paths))
		hashes := make([]string, 0, end-start)
		for _, p := range paths[start:end] {
			hashes = append(hashes, model.HashS3ObjectPath(p))
		}
		var rows []model.S3ObjectMeta
		if err := db.Where("path_hash IN ?", hashes).Find(&rows).Error; err != nil {
			return nil, errors.WithStack(err)
		}
		for i := range rows {
			res[rows[i].Path] = &rows[i]
		}
	}
	return res, nil
}

func SaveS3ObjectMeta(m *model.S3ObjectMeta) error {
	m.PathHash = model.HashS3ObjectPath(m.Path)
	return errors.WithStack(db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "path_hash"}},
		DoUpdates: clause.AssignmentColumns([]string{"path", "size", "e_tag", "meta", "updated_at"}),
	}).Create(m).Error)
}

// DeleteS3ObjectMetaTree removes the record for path and for everything below it.
func DeleteS3ObjectMetaTree(path string) error {
	path = strings.TrimSuffix(path, "/")
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(path)
	return errors.WithStack(db.Where("path_hash = ? OR path LIKE ? ESCAPE '\\'",
		model.HashS3ObjectPath(path), escaped+"/%").Delete(&model.S3ObjectMeta{}).Error)
}

func DeleteS3ObjectMeta(path string) error {
	return errors.WithStack(db.Where("path_hash = ?", model.HashS3ObjectPath(path)).Delete(&model.S3ObjectMeta{}).Error)
}
