package wait0

type statsCacheIndex struct {
	s *Service
}

func (i statsCacheIndex) RAMKeys() []string {
	return i.s.ram.Keys()
}

func (i statsCacheIndex) DiskKeyCount() int {
	return i.s.disk.KeyCount()
}

func (i statsCacheIndex) DiskHasKey(key string) bool {
	return i.s.disk.HasKey(key)
}

func (i statsCacheIndex) RAMTotalSize() uint64 {
	return uint64(i.s.ram.TotalSize())
}

func (i statsCacheIndex) DiskTotalSize() uint64 {
	return uint64(i.s.disk.TotalSize())
}
