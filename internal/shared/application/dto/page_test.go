package dto_test

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	"myapp.v1.0.0/internal/shared/application/dto"
)

func TestNewPageQueryNormalizes(t *testing.T) {
	tests := []struct {
		page, size         int
		wantPage, wantSize int
		wantOffset         int
	}{
		{page: 1, size: 20, wantPage: 1, wantSize: 20, wantOffset: 0},
		{page: 3, size: 10, wantPage: 3, wantSize: 10, wantOffset: 20},
		{page: 0, size: 10, wantPage: 1, wantSize: 10, wantOffset: 0},
		{page: -5, size: 10, wantPage: 1, wantSize: 10, wantOffset: 0},
		{page: 2, size: 0, wantPage: 2, wantSize: dto.DefaultPageSize, wantOffset: dto.DefaultPageSize},
		{page: 1, size: 500, wantPage: 1, wantSize: dto.MaxPageSize, wantOffset: 0},
		{page: 1, size: -1, wantPage: 1, wantSize: 1, wantOffset: 0},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.page)+"/"+strconv.Itoa(tt.size), func(t *testing.T) {
			q := dto.NewPageQuery(tt.page, tt.size)
			assert.Equal(t, tt.wantPage, q.Page())
			assert.Equal(t, tt.wantSize, q.Size())
			assert.Equal(t, tt.wantSize, q.Limit())
			assert.Equal(t, tt.wantOffset, q.Offset())
		})
	}
}

func TestNewPageResultComputesTotalPages(t *testing.T) {
	q := dto.NewPageQuery(2, 20)

	assert.Equal(t, 3, dto.NewPageResult(q, []int{1}, 57).TotalPages)
	assert.Equal(t, 1, dto.NewPageResult(q, []int{1}, 20).TotalPages)
	assert.Equal(t, 0, dto.NewPageResult[int](q, nil, 0).TotalPages)

	p := dto.NewPageResult(q, []int{1}, 57)
	assert.Equal(t, 2, p.Page, "la página del resultado usa la misma numeración desde 1")
}

func TestNewPageResultNeverReturnsNilItems(t *testing.T) {
	p := dto.NewPageResult[int](dto.NewPageQuery(1, 10), nil, 0)
	assert.NotNil(t, p.Items)
}

func TestMapPageKeepsMetadata(t *testing.T) {
	p := dto.NewPageResult(dto.NewPageQuery(2, 2), []int{1, 2}, 5)

	m := dto.MapPage(p, strconv.Itoa)

	assert.Equal(t, []string{"1", "2"}, m.Items)
	assert.Equal(t, p.Page, m.Page)
	assert.Equal(t, p.TotalPages, m.TotalPages)
	assert.Equal(t, p.TotalElements, m.TotalElements)
}

func TestPrincipalHasScope(t *testing.T) {
	p := dto.Principal{Scopes: []string{"example:read", "example:write"}}

	assert.True(t, p.HasScope("example:write"))
	assert.False(t, p.HasScope("example:admin"))
}

func TestPageQueryNeverOverflowsOffset(t *testing.T) {
	q := dto.NewPageQuery(4611686018427387905, 4) // (page-1)*size desbordaría int64

	assert.Positive(t, q.Offset())
	assert.LessOrEqual(t, q.Offset(), dto.MaxOffset)
	assert.Equal(t, dto.MaxPage(4), q.Page())
}

func TestTotalPagesNeverExceedsTheLastReachablePage(t *testing.T) {
	p := dto.NewPageResult(dto.NewPageQuery(1, 20), []int{}, dto.MaxCount)

	assert.EqualValues(t, dto.MaxCount, p.TotalElements)
	assert.Equal(t, dto.MaxPage(20), p.TotalPages, "el cliente no ve páginas que después responderían 400")
}
