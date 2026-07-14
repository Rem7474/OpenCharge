package scraper

import (
	"encoding/json"
	"os"
	"time"
)

type artifactMeta struct {
	Count     int       `json:"count"`
	Completed bool      `json:"completed"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func metaPath(path string) string {
	return path + ".meta.json"
}

func loadArtifactMeta(path string) (artifactMeta, bool, error) {
	file, err := os.Open(metaPath(path))
	if err != nil {
		if os.IsNotExist(err) {
			return artifactMeta{}, false, nil
		}
		return artifactMeta{}, false, err
	}
	defer file.Close()

	var meta artifactMeta
	if err := json.NewDecoder(file).Decode(&meta); err != nil {
		return artifactMeta{}, false, err
	}
	return meta, true, nil
}

func saveArtifactMeta(path string, count int) error {
	file, err := os.Create(metaPath(path))
	if err != nil {
		return err
	}
	defer file.Close()

	meta := artifactMeta{
		Count:     count,
		Completed: true,
		UpdatedAt: time.Now().UTC(),
	}
	return json.NewEncoder(file).Encode(meta)
}
