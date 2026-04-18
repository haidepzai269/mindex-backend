package utils

import (
	"hash/fnv"
	"math"
)

type BloomFilter struct {
	bitset []uint64
	m      uint64 // number of bits
	k      uint32 // number of hash functions
}

func NewBloomFilter(n uint64, p float64) *BloomFilter {
	// m = - (n * ln(p)) / (ln(2)^2)
	m := uint64(math.Ceil(-float64(n) * math.Log(p) / math.Pow(math.Log(2), 2)))
	// k = (m/n) * ln(2)
	k := uint32(math.Ceil(float64(m) / float64(n) * math.Log(2)))

	return &BloomFilter{
		bitset: make([]uint64, (m+63)/64),
		m:      m,
		k:      k,
	}
}

func (bf *BloomFilter) Add(data string) {
	h1, h2 := bf.hash(data)
	for i := uint32(0); i < bf.k; i++ {
		index := (h1 + uint64(i)*h2) % bf.m
		bf.bitset[index/64] |= (1 << (index % 64))
	}
}

func (bf *BloomFilter) Test(data string) bool {
	h1, h2 := bf.hash(data)
	for i := uint32(0); i < bf.k; i++ {
		index := (h1 + uint64(i)*h2) % bf.m
		if (bf.bitset[index/64] & (1 << (index % 64))) == 0 {
			return false
		}
	}
	return true
}

func (bf *BloomFilter) hash(data string) (uint64, uint64) {
	h := fnv.New64a()
	h.Write([]byte(data))
	h1 := h.Sum64()

	h.Write([]byte("bloom")) // double hash salt
	h2 := h.Sum64()

	return h1, h2
}

var EmailBloom *BloomFilter

func InitEmailBloom(emails []string) {
	// Dự kiến 100,000 users, tỷ lệ false positive 0.1%
	EmailBloom = NewBloomFilter(100000, 0.001)
	for _, email := range emails {
		EmailBloom.Add(email)
	}
}
