/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package fi

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"k8s.io/kops/upup/pkg/fi/utils"
	"k8s.io/kops/util/pkg/hashing"

	"github.com/golang/glog"
)

type asset struct {
	Key       string
	AssetPath string
	resource  Resource
	source    *Source
}

type Source struct {
	Parent             *Source
	URL                string
	Hash               *hashing.Hash
	ExtractFromArchive string
}

// Builds a unique key for this source
func (s *Source) Key() string {
	var k string
	if s.Parent != nil {
		k = s.Parent.Key() + "/"
	}
	if s.URL != "" {
		k += s.URL
	} else if s.ExtractFromArchive != "" {
		k += s.ExtractFromArchive
	} else {
		glog.Fatalf("expected either URL or ExtractFromArchive to be set")
	}
	return k
}

func (s *Source) String() string {
	return "Source[" + s.Key() + "]"
}

type HasSource interface {
	GetSource() *Source
}

// assetResource implements Resource, but also implements HasFetchInstructions
type assetResource struct {
	asset *asset
}

var _ Resource = &assetResource{}
var _ HasSource = &assetResource{}

func (r *assetResource) Open() (io.Reader, error) {
	return r.asset.resource.Open()
}

func (r *assetResource) GetSource() *Source {
	return r.asset.source
}

type AssetStore struct {
	cacheDir string
	assets   []*asset
}

func NewAssetStore(cacheDir string) *AssetStore {
	a := &AssetStore{
		cacheDir: cacheDir,
	}
	return a
}
func (a *AssetStore) Find(key string, assetPath string) (Resource, error) {
	var matches []*asset
	for _, asset := range a.assets {
		if asset.Key != key {
			continue
		}

		if assetPath != "" {
			if !strings.HasSuffix(asset.AssetPath, assetPath) {
				continue
			}
		}

		matches = append(matches, asset)
	}

	if len(matches) == 0 {
		return nil, nil
	}
	if len(matches) == 1 {
		glog.Infof("Resolved asset %s:%s to %s", key, assetPath, matches[0].AssetPath)
		return &assetResource{asset: matches[0]}, nil
	}

	glog.Infof("Matching assets:")
	for _, match := range matches {
		glog.Infof("    %s %s", match.Key, match.AssetPath)
	}
	return nil, fmt.Errorf("found multiple matching assets for key: %q", key)
}

func hashFromHttpHeader(url string) (*hashing.Hash, error) {
	glog.Infof("Doing HTTP HEAD on %q", url)
	response, err := http.Head(url)
	if err != nil {
		return nil, fmt.Errorf("error doing HEAD on %q: %v", url, err)
	}
	defer response.Body.Close()

	etag := response.Header.Get("ETag")
	etag = strings.TrimSpace(etag)
	etag = strings.Trim(etag, "'\"")

	if etag != "" {
		if len(etag) == 32 {
			// Likely md5
			return hashing.HashAlgorithmMD5.FromString(etag)
		}
	}

	return nil, fmt.Errorf("unable to determine hash from HTTP HEAD: %q", url)
}

// Add an asset into the store, in one of the recognized formats (see Assets in types package)
func (a *AssetStore) Add(id string) error {
	if strings.HasPrefix(id, "http://") || strings.HasPrefix(id, "https://") {
		return a.addURL(id, nil)
	}
	i := strings.Index(id, "@http://")
	if i == -1 {
		i = strings.Index(id, "@https://")
	}
	if i != -1 {
		url := id[i+1:]
		hash, err := hashing.FromString(id[:i])
		if err != nil {
			return err
		}
		return a.addURL(url, hash)
	}
	// TODO: local files!
	return fmt.Errorf("unknown asset format: %q", id)
}

func (a *AssetStore) addURL(url string, hash *hashing.Hash) error {
	var err error

	if hash == nil {
		hash, err = hashFromHttpHeader(url)
		if err != nil {
			return err
		}
	}

	localFile := path.Join(a.cacheDir, hash.String()+"_"+utils.SanitizeString(url))
	_, err = DownloadURL(url, localFile, hash)
	if err != nil {
		return err
	}

	key := path.Base(url)
	assetPath := url
	r := NewFileResource(localFile)

	source := &Source{URL: url, Hash: hash}

	asset := &asset{
		Key:       key,
		AssetPath: assetPath,
		resource:  r,
		source:    source,
	}
	glog.V(2).Infof("added asset %q for %q", asset.Key, asset.resource)
	a.assets = append(a.assets, asset)

	if strings.HasSuffix(assetPath, ".tar.gz") {
		err = a.addArchive(source, localFile)
		if err != nil {
			return err
		}
	}

	return nil
}

//func (a *AssetStore) addFile(assetPath string, p string) error {
//	r := NewFileResource(p)
//	return a.addResource(assetPath, r)
//}

//func (a *AssetStore) addResource(assetPath string, r Resource) error {
//	hash, err := HashForResource(r, HashAlgorithmSHA256)
//	if err != nil {
//		return err
//	}
//
//	localFile := path.Join(a.assetDir, hash + "_" + utils.SanitizeString(assetPath))
//	hasHash, err := fileHasHash(localFile, hash)
//	if err != nil {
//		return err
//	}
//
//	if !hasHash {
//		err = WriteFile(localFile, r, 0644, 0755)
//		if err != nil {
//			return err
//		}
//	}
//
//	asset := &asset{
//		Key:       localFile,
//		AssetPath: assetPath,
//		resource:  r,
//	}
//	glog.V(2).Infof("added asset %q for %q", asset.Key, asset.resource)
//	a.assets = append(a.assets, asset)
//
//	if strings.HasSuffix(assetPath, ".tar.gz") {
//		err = a.addArchive(localFile)
//		if err != nil {
//			return err
//		}
//	}
//
//	return nil
//}

func (a *AssetStore) addArchive(archiveSource *Source, archiveFile string) error {
	extracted := path.Join(a.cacheDir, "extracted/"+path.Base(archiveFile))

	// TODO: Use a temp file so this is atomic
	if _, err := os.Stat(extracted); os.IsNotExist(err) {
		err := os.MkdirAll(extracted, 0755)
		if err != nil {
			return fmt.Errorf("error creating directories %q: %v", path.Dir(extracted), err)
		}

		args := []string{"tar", "zxf", archiveFile, "-C", extracted}
		glog.Infof("running extract command %s", args)
		cmd := exec.Command(args[0], args[1:]...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("error expanding asset file %q %v: %s", archiveFile, err, string(output))
		}
	}

	localBase := extracted
	assetBase := ""

	walker := func(localPath string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("error descending into path %q: %v", localPath, err)
		}

		if info.IsDir() {
			return nil
		}

		relativePath, err := filepath.Rel(localBase, localPath)
		if err != nil {
			return fmt.Errorf("error finding relative path for %q: %v", localPath, err)
		}

		assetPath := path.Join(assetBase, relativePath)
		key := info.Name()
		r := NewFileResource(localPath)

		asset := &asset{
			Key:       key,
			AssetPath: assetPath,
			resource:  r,
			source:    &Source{Parent: archiveSource, ExtractFromArchive: assetPath},
		}
		glog.V(2).Infof("added asset %q for %q", asset.Key, asset.resource)
		a.assets = append(a.assets, asset)

		return nil
	}

	err := filepath.Walk(localBase, walker)
	if err != nil {
		return fmt.Errorf("error adding expanded asset files in %q: %v", extracted, err)
	}
	return nil

}
