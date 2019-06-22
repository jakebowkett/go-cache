package cache

import (
	"errors"
	"fmt"
	"html/template"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Object struct {
	notFile bool
	path    string
	data    []byte
	lastMod time.Time
}

func (o *Object) add(data []byte, lastMod time.Time) {
	o.data = append(o.data, data...)
	if lastMod.After(o.lastMod) {
		o.lastMod = lastMod
	}
}

// Data returns a copy of the data held in Object.
func (o *Object) Bytes() []byte {
	return o.data[:]
}

func (o *Object) HTML() template.HTML {
	return template.HTML(o.data[:])
}

func (o *Object) CSS() template.CSS {
	return template.CSS(o.data[:])
}

func (o *Object) String() string {
	return string(o.data)
}

func (o *Object) LastMod() time.Time {
	return o.lastMod
}

type Cache struct {
	mu      sync.RWMutex
	mapping map[string]*Object
	size    int64

	/*
		MaxSize represents how large Cache may grow in bytes.
		The size is calculated as the sum of all data it has
		stored.

		Metadata the Object might hold (such as the last time
		a file was modified) does not count toward this size.
		Therefore the precise memory footprint of Cache will
		always be larger than MaxSize.

		MaxSize defaults to 0 which is treated as infinite.
	*/
	maxSize int64
	onLoad  func(ext string, file []byte) []byte
}

func New() *Cache {
	return &Cache{mapping: make(map[string]*Object)}
}

func (c *Cache) MaxSize(n int64) {
	c.maxSize = n
}

func (c *Cache) OnLoad(callback func(ext string, file []byte) []byte) {
	c.onLoad = callback
}

func (c *Cache) List() (aliases []string) {
	for alias := range c.mapping {
		aliases = append(aliases, alias)
	}
	return aliases
}

func (c *Cache) MustConcatDir(alias, dirPath string, exts []string, recursive bool) {
	if err := c.ConcatDir(alias, dirPath, exts, recursive); err != nil {
		panic(err)
	}
}

func (c *Cache) MustAddDir(alias, dirPath string, exts []string, recursive bool) {
	if err := c.AddDir(alias, dirPath, exts, recursive); err != nil {
		panic(err)
	}
}

func (c *Cache) MustAddFile(alias, filePath string) {
	if err := c.AddFile(alias, filePath); err != nil {
		panic(err)
	}
}

func (c *Cache) AddString(alias, s string) {
	c.mu.Lock()
	c.mapping[alias] = &Object{
		data:    []byte(s),
		notFile: true,
		lastMod: time.Now(),
	}
	c.mu.Unlock()
}

func (c *Cache) ConcatDir(alias, dirPath string, exts []string, recursive bool) error {
	obj := &Object{}
	err := c.concatDir(alias, dirPath, exts, recursive, obj)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.mapping[alias] = obj
	c.size += int64(len(obj.data))
	c.mu.Unlock()
	return nil
}

func (c *Cache) concatDir(alias, dirPath string, exts []string, recursive bool, obj *Object) error {

	dirPath, err := filepath.Abs(dirPath)
	if err != nil {
		return err
	}

	dir, err := ioutil.ReadDir(dirPath)
	if err != nil {
		return err
	}

	var file []byte
	var size int64
	var lastMod time.Time

	for _, info := range dir {

		dirPath := filepath.Join(dirPath, info.Name())

		if info.IsDir() && recursive {
			c.concatDir(alias, dirPath, exts, recursive, obj)
			continue
		}

		if !info.Mode().IsRegular() {
			continue
		}

		ext := filepath.Ext(info.Name())
		if len(exts) > 0 && !in(exts, ext) {
			continue
		}

		size += info.Size()
		if c.maxSize > 0 && c.size+size > c.maxSize {
			return errors.New(fmt.Sprintf(
				"cache exceeded MaxSize (%d bytes)", c.maxSize))
		}

		if info.ModTime().After(lastMod) {
			lastMod = info.ModTime()
		}

		f, err := ioutil.ReadFile(dirPath)
		if err != nil {
			return err
		}

		if c.onLoad != nil {
			f = c.onLoad(filepath.Ext(dirPath), f)
		}

		file = append(file, f...)
	}

	obj.data = append(obj.data, file...)
	obj.notFile = true
	if lastMod.After(obj.lastMod) {
		obj.lastMod = lastMod
	}

	return nil
}

func (c *Cache) AddDir(alias, dirPath string, exts []string, recursive bool) error {

	dirPath, err := filepath.Abs(dirPath)
	if err != nil {
		return err
	}

	dir, err := ioutil.ReadDir(dirPath)
	if err != nil {
		return err
	}

	for _, info := range dir {

		alias := alias + "/" + info.Name()
		dirPath := filepath.Join(dirPath, info.Name())

		if info.IsDir() && recursive {
			c.AddDir(alias, dirPath, exts, recursive)
			continue
		}

		if !info.Mode().IsRegular() {
			continue
		}

		ext := filepath.Ext(info.Name())
		if len(exts) > 0 && !in(exts, ext) {
			continue
		}

		err := c.AddFile(alias, dirPath)
		if err != nil {
			return err
		}
	}

	return nil
}

func in(ss []string, s string) bool {
	for i := range ss {
		if ss[i] == s {
			return true
		}
	}
	return false
}

func (c *Cache) AddFile(alias, filePath string) error {

	filePath, err := filepath.Abs(filePath)
	if err != nil {
		return err
	}

	info, err := os.Stat(filePath)
	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() {
		return errors.New(fmt.Sprintf("%s is not a file", filePath))
	}

	if c.maxSize > 0 && c.size+info.Size() > c.maxSize {
		return errors.New(fmt.Sprintf(
			"cache exceeded MaxSize (%d bytes)", c.maxSize))
	}

	f, err := ioutil.ReadFile(filePath)
	if err != nil {
		return err
	}

	if c.onLoad != nil {
		f = c.onLoad(filepath.Ext(filePath), f)
	}

	c.mu.Lock()
	c.mapping[alias] = &Object{
		path:    filePath,
		data:    f,
		lastMod: info.ModTime(),
	}
	c.size += int64(len(f))
	c.mu.Unlock()

	return nil
}

func (c *Cache) Delete(alias string) {
	c.mu.Lock()
	c.delete(alias, nil)
	c.mu.Unlock()
}

func (c *Cache) delete(alias string, dropped []string) {
	c.size -= int64(len(c.mapping[alias].data))
	delete(c.mapping, alias)
	if dropped != nil {
		dropped = append(dropped, alias)
	}
}

func (c *Cache) Load(alias string) *Object {
	c.mu.RLock()
	f, ok := c.mapping[alias]
	c.mu.RUnlock()
	if !ok {
		return nil
	}
	return f
}

func (c *Cache) LoadDir(dir string) (objects []*Object) {
	c.mu.RLock()
	for path, o := range c.mapping {
		if strings.HasPrefix(path, dir) {
			objects = append(objects, o)
		}
	}
	c.mu.RUnlock()
	return objects
}

func (c *Cache) Empty() {
	c.mu.Lock()
	c.mapping = make(map[string]*Object)
	c.mu.Unlock()
}

func (c *Cache) Refresh() (dropped []string) {

	// Ensure dropped is non-nil for
	// calls to c.delete
	dropped = []string{}

	c.mu.Lock()
	defer c.mu.Unlock()

	for alias, file := range c.mapping {

		if file.notFile {
			continue
		}

		info, err := os.Stat(file.path)
		if err != nil {
			c.delete(alias, dropped)
			continue
		}

		if !info.Mode().IsRegular() {
			c.delete(alias, dropped)
			continue
		}

		if !info.ModTime().After(file.lastMod) {
			continue
		}

		f, err := ioutil.ReadFile(file.path)
		if err != nil {
			c.delete(alias, dropped)
			continue
		}

		if c.onLoad != nil {
			f = c.onLoad(filepath.Ext(file.path), f)
		}

		file.data = f
		file.lastMod = info.ModTime()
	}

	return dropped
}
