package pkginfo

import (
	"compress/bzip2"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// A UDIF disk image (hdiutil create -format UDZO) is a sequence of compressed
// chunks, a "koly" trailer in the last 512 bytes and an XML plist whose
// "blkx" entries map each partition's sectors to chunks. MacDMG decompresses
// only the chunks it needs from the Apple_HFS partition and walks the HFS+
// catalog to the app bundle.

const sector = 512

// MacDMG reads the first *.app in the DMG's top folder.
func MacDMG(file string) (*Info, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	part, err := openHFSPartition(f)
	if err != nil {
		return nil, err
	}
	vol, err := openHFS(part)
	if err != nil {
		return nil, err
	}

	app, err := vol.find(hfsRootID, func(e catEntry) bool { return e.folder && strings.HasSuffix(e.name, ".app") })
	if err != nil {
		return nil, errors.New("DMG 里没有 .app，请上传 build_macos.sh 打出的安装包")
	}
	contents, err := vol.child(app.id, "Contents")
	if err != nil {
		return nil, fmt.Errorf("%s 不完整", app.name)
	}
	raw, err := vol.readChild(contents.id, "Info.plist", 1<<20)
	if err != nil {
		return nil, fmt.Errorf("%s 缺少 Info.plist", app.name)
	}
	pl, err := parsePlist(raw)
	if err != nil {
		return nil, fmt.Errorf("Info.plist：%w", err)
	}
	m, _ := pl.(map[string]any)
	info := &Info{
		Version: plistString(m, "CFBundleShortVersionString"),
		MinOS:   plistString(m, "LSMinimumSystemVersion"),
	}
	info.Build, _ = strconv.ParseInt(plistString(m, "CFBundleVersion"), 10, 64)
	if info.Version == "" || info.Build <= 0 {
		return nil, errors.New("Info.plist 里没有版本号或构建号")
	}

	macos, err := vol.child(contents.id, "MacOS")
	if err != nil {
		return nil, fmt.Errorf("%s 缺少 Contents/MacOS", app.name)
	}
	exe := plistString(m, "CFBundleExecutable")
	head, err := vol.readChild(macos.id, exe, 4096)
	if err != nil {
		return nil, fmt.Errorf("找不到主程序 %s", exe)
	}
	if info.Archs, err = machoArchs(head); err != nil {
		return nil, fmt.Errorf("%s：%w", exe, err)
	}
	coreHead, err := vol.readChild(macos.id, "huanyou-core", 4096)
	if err != nil {
		return nil, errors.New("App 里缺少 VPN 核心 huanyou-core（Contents/MacOS）")
	}
	coreArchs, err := machoArchs(coreHead)
	if err != nil {
		return nil, fmt.Errorf("huanyou-core：%w", err)
	}
	for _, a := range info.Archs {
		if !has(coreArchs, a) {
			return nil, fmt.Errorf("VPN 核心 huanyou-core 不支持 %s，请用 mobile/build.sh macos 重新编译", a)
		}
	}
	return info, nil
}

// ---- Mach-O ----

func machoArchs(b []byte) ([]string, error) {
	if len(b) < 8 {
		return nil, errors.New("不是 macOS 程序")
	}
	name := func(cpu uint32) string {
		switch cpu {
		case 0x0100000c:
			return ArchARM64
		case 0x01000007:
			return ArchX64
		case 7:
			return ArchX86
		}
		return fmt.Sprintf("cpu-%x", cpu)
	}
	be := binary.BigEndian
	switch be.Uint32(b) {
	case 0xcafebabe, 0xcafebabf: // fat / fat64
		n := int(be.Uint32(b[4:]))
		size := 20
		if be.Uint32(b) == 0xcafebabf {
			size = 32
		}
		if n <= 0 || n > 16 || 8+n*size > len(b) {
			return nil, errors.New("通用二进制头损坏")
		}
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, name(be.Uint32(b[8+i*size:])))
		}
		return sortedUnique(out), nil
	case 0xcffaedfe, 0xcefaedfe: // thin, little-endian magic
		return []string{name(binary.LittleEndian.Uint32(b[4:]))}, nil
	}
	return nil, errors.New("不是 macOS 程序")
}

// ---- UDIF ----

type udifChunk struct {
	typ          uint32
	start, count int64 // sectors, relative to the partition
	offset, clen int64 // compressed bytes in the file
}

type udifPartition struct {
	f      io.ReaderAt
	chunks []udifChunk
	size   int64
	cache  map[int][]byte // chunk index -> decompressed data
	order  []int
}

func openHFSPartition(f *os.File) (*udifPartition, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() < sector {
		return nil, errors.New("文件不是有效的 DMG 磁盘映像")
	}
	koly := make([]byte, sector)
	if _, err := f.ReadAt(koly, st.Size()-sector); err != nil {
		return nil, err
	}
	if string(koly[:4]) != "koly" {
		return nil, errors.New("文件不是有效的 DMG 磁盘映像")
	}
	be := binary.BigEndian
	dataFork := int64(be.Uint64(koly[24:]))
	xmlOff, xmlLen := int64(be.Uint64(koly[216:])), int64(be.Uint64(koly[224:]))
	if xmlLen <= 0 || xmlLen > 64<<20 || xmlOff+xmlLen > st.Size() {
		return nil, errors.New("DMG 缺少分区表")
	}
	raw := make([]byte, xmlLen)
	if _, err := f.ReadAt(raw, xmlOff); err != nil {
		return nil, err
	}
	pl, err := parsePlist(raw)
	if err != nil {
		return nil, fmt.Errorf("DMG 分区表：%w", err)
	}
	root, _ := pl.(map[string]any)
	rsrc, _ := root["resource-fork"].(map[string]any)
	blkx, _ := rsrc["blkx"].([]any)
	for _, x := range blkx {
		e, _ := x.(map[string]any)
		name := plistString(e, "Name") + plistString(e, "CFName")
		data, _ := e["Data"].([]byte)
		if !strings.Contains(name, "Apple_HFS") || len(data) < 204 || string(data[:4]) != "mish" {
			continue
		}
		p := &udifPartition{f: f, size: int64(be.Uint64(data[16:])) * sector, cache: map[int][]byte{}}
		base := dataFork + int64(be.Uint64(data[24:]))
		n := int(be.Uint32(data[200:]))
		for i := 0; i < n && 204+(i+1)*40 <= len(data); i++ {
			c := data[204+i*40:]
			ch := udifChunk{
				typ: be.Uint32(c), start: int64(be.Uint64(c[8:])), count: int64(be.Uint64(c[16:])),
				offset: base + int64(be.Uint64(c[24:])), clen: int64(be.Uint64(c[32:])),
			}
			if ch.typ != 0x7ffffffe && ch.typ != 0xffffffff && ch.count > 0 {
				p.chunks = append(p.chunks, ch)
			}
		}
		sort.Slice(p.chunks, func(i, j int) bool { return p.chunks[i].start < p.chunks[j].start })
		return p, nil
	}
	return nil, errors.New("DMG 里没有 HFS+ 分区，请用 build_macos.sh 打包")
}

// ReadAt reads partition bytes, decompressing the chunks involved.
func (p *udifPartition) ReadAt(b []byte, off int64) (int, error) {
	n := 0
	for n < len(b) {
		pos := off + int64(n)
		if pos >= p.size {
			return n, io.EOF
		}
		sec := pos / sector
		i := sort.Search(len(p.chunks), func(i int) bool { return p.chunks[i].start+p.chunks[i].count > sec })
		if i == len(p.chunks) || p.chunks[i].start > sec {
			return n, fmt.Errorf("DMG 扇区 %d 不存在", sec)
		}
		data, err := p.chunk(i)
		if err != nil {
			return n, err
		}
		n += copy(b[n:], data[pos-p.chunks[i].start*sector:])
	}
	return n, nil
}

func (p *udifPartition) chunk(i int) ([]byte, error) {
	if d, ok := p.cache[i]; ok {
		return d, nil
	}
	c := p.chunks[i]
	size := c.count * sector
	if size > 64<<20 {
		return nil, errors.New("DMG 数据块过大")
	}
	out := make([]byte, size)
	src := io.NewSectionReader(p.f, c.offset, c.clen)
	switch c.typ {
	case 0, 2: // zero fill / ignored
	case 1: // raw
		if _, err := io.ReadFull(src, out); err != nil {
			return nil, err
		}
	case 0x80000005: // zlib (UDZO)
		zr, err := zlib.NewReader(src)
		if err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(zr, out); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, err
		}
	case 0x80000006: // bzip2 (UDBZ)
		if _, err := io.ReadFull(bzip2.NewReader(src), out); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("不支持的 DMG 压缩格式（0x%x），请用 build_macos.sh 打包（UDZO）", c.typ)
	}
	// Keep a few chunks: catalog nodes are read many times.
	if len(p.order) >= 8 {
		delete(p.cache, p.order[0])
		p.order = p.order[1:]
	}
	p.cache[i] = out
	p.order = append(p.order, i)
	return out, nil
}

// ---- HFS+ ----

const hfsRootID = 2

type hfsExtent struct{ start, count uint32 }

type hfsFork struct {
	size    int64
	extents [8]hfsExtent
}

type catEntry struct {
	name   string
	folder bool
	id     uint32
	data   hfsFork
}

type hfsVolume struct {
	r         io.ReaderAt
	blockSize int64
	children  map[uint32][]catEntry
}

func readFork(b []byte) hfsFork {
	be := binary.BigEndian
	f := hfsFork{size: int64(be.Uint64(b))}
	for i := range f.extents {
		f.extents[i] = hfsExtent{be.Uint32(b[16+i*8:]), be.Uint32(b[20+i*8:])}
	}
	return f
}

func openHFS(r io.ReaderAt) (*hfsVolume, error) {
	hdr := make([]byte, 512)
	if _, err := r.ReadAt(hdr, 1024); err != nil {
		return nil, err
	}
	if sig := string(hdr[:2]); sig != "H+" && sig != "HX" {
		return nil, errors.New("DMG 不是 HFS+ 格式，请用 build_macos.sh 打包")
	}
	be := binary.BigEndian
	v := &hfsVolume{r: r, blockSize: int64(be.Uint32(hdr[40:])), children: map[uint32][]catEntry{}}
	catalog, err := v.read(readFork(hdr[272:]), -1)
	if err != nil {
		return nil, fmt.Errorf("读取 HFS+ 目录：%w", err)
	}
	return v, v.loadCatalog(catalog)
}

// read returns the fork's data (at most limit bytes; -1 = all). Only the 8
// extents in the record are used, which covers files in a fresh image.
func (v *hfsVolume) read(f hfsFork, limit int64) ([]byte, error) {
	want := f.size
	if limit >= 0 && want > limit {
		want = limit
	}
	if want > 256<<20 {
		return nil, errors.New("文件过大")
	}
	out := make([]byte, 0, want)
	for _, e := range f.extents {
		if int64(len(out)) >= want || e.count == 0 {
			break
		}
		n := min(int64(e.count)*v.blockSize, want-int64(len(out)))
		buf := make([]byte, n)
		if _, err := v.r.ReadAt(buf, int64(e.start)*v.blockSize); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		out = append(out, buf...)
	}
	if int64(len(out)) < want {
		return nil, errors.New("文件碎片过多")
	}
	return out, nil
}

// loadCatalog indexes every leaf record of the catalog B-tree by parent folder.
func (v *hfsVolume) loadCatalog(cat []byte) error {
	be := binary.BigEndian
	if len(cat) < 512 {
		return errors.New("HFS+ 目录为空")
	}
	nodeSize := int(be.Uint16(cat[32:]))
	if nodeSize < 512 || len(cat) < nodeSize {
		return errors.New("HFS+ 目录损坏")
	}
	node := be.Uint32(cat[24:]) // first leaf
	for seen := 0; node != 0 && seen < len(cat)/nodeSize; seen++ {
		off := int(node) * nodeSize
		if off+nodeSize > len(cat) {
			return errors.New("HFS+ 目录损坏")
		}
		nd := cat[off : off+nodeSize]
		if int8(nd[8]) != -1 { // leaf node
			return errors.New("HFS+ 目录损坏")
		}
		for i := 0; i < int(be.Uint16(nd[10:])); i++ {
			ro := int(be.Uint16(nd[nodeSize-2*(i+1):]))
			if ro+8 > nodeSize {
				continue
			}
			keyLen := int(be.Uint16(nd[ro:]))
			parent := be.Uint32(nd[ro+2:])
			nameLen := int(be.Uint16(nd[ro+6:]))
			rec := ro + 2 + keyLen
			if ro+8+nameLen*2 > nodeSize || rec+2 > nodeSize {
				continue
			}
			u := make([]rune, nameLen)
			for k := range u {
				u[k] = rune(be.Uint16(nd[ro+8+k*2:]))
			}
			e := catEntry{name: string(u)}
			switch typ := be.Uint16(nd[rec:]); {
			case typ == 1 && rec+88 <= nodeSize: // folder record
				e.folder, e.id = true, be.Uint32(nd[rec+8:])
			case typ == 2 && rec+248 <= nodeSize: // file record, data fork at 88
				e.id, e.data = be.Uint32(nd[rec+8:]), readFork(nd[rec+88:])
			default:
				continue // thread records
			}
			v.children[parent] = append(v.children[parent], e)
		}
		node = be.Uint32(nd) // fLink
	}
	return nil
}

func (v *hfsVolume) find(parent uint32, match func(catEntry) bool) (catEntry, error) {
	for _, e := range v.children[parent] {
		if match(e) {
			return e, nil
		}
	}
	return catEntry{}, errNotFound
}

func (v *hfsVolume) child(parent uint32, name string) (catEntry, error) {
	return v.find(parent, func(e catEntry) bool { return e.name == name })
}

func (v *hfsVolume) readChild(parent uint32, name string, limit int64) ([]byte, error) {
	e, err := v.child(parent, name)
	if err != nil || e.folder {
		return nil, errNotFound
	}
	if e.data.size == 0 {
		return nil, errors.New("文件为空或被压缩")
	}
	return v.read(e.data, limit)
}
