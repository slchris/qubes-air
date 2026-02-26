// Package handler provides common utilities for HTTP handlers.
package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// ErrorResponse represents a standard error response.
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
	Code    int    `json:"code"`
}

// respondError sends a standardized error response.
func respondError(c *gin.Context, code int, err error) {
	c.JSON(code, ErrorResponse{
		Error:   http.StatusText(code),
		Message: err.Error(),
		Code:    code,
	})
}

// PaginationParams holds pagination query parameters.
type PaginationParams struct {
	Page     int
	PageSize int
	Offset   int
}

// DefaultPagination returns default pagination values.
func DefaultPagination() PaginationParams {
	return PaginationParams{
		Page:     1,
		PageSize: 20,
		Offset:   0,
	}
}

// ParsePagination extracts pagination params from query string.
func ParsePagination(c *gin.Context) PaginationParams {
	params := DefaultPagination()

	if page := c.Query("page"); page != "" {
		if p, err := strconv.Atoi(page); err == nil && p > 0 {
			params.Page = p
		}
	}

	if pageSize := c.Query("page_size"); pageSize != "" {
		if ps, err := strconv.Atoi(pageSize); err == nil && ps > 0 && ps <= 100 {
			params.PageSize = ps
		}
	}

	params.Offset = (params.Page - 1) * params.PageSize
	return params
}

// SortParams holds sorting query parameters.
type SortParams struct {
	Field string
	Order string // "asc" or "desc"
}

// DefaultSort returns default sort values.
func DefaultSort(defaultField string) SortParams {
	return SortParams{
		Field: defaultField,
		Order: "asc",
	}
}

// ParseSort extracts sort params from query string.
func ParseSort(c *gin.Context, defaultField string, allowedFields []string) SortParams {
	params := DefaultSort(defaultField)

	if sortField := c.Query("sort"); sortField != "" {
		if isAllowedField(sortField, allowedFields) {
			params.Field = sortField
		}
	}

	if order := c.Query("order"); order == "desc" {
		params.Order = "desc"
	}

	return params
}

// isAllowedField checks if a field is in the allowed list.
func isAllowedField(field string, allowed []string) bool {
	for _, f := range allowed {
		if f == field {
			return true
		}
	}
	return false
}

// ListResponse represents a paginated list response.
type ListResponse struct {
	Data       interface{}    `json:"data"`
	Pagination PaginationMeta `json:"pagination"`
	Sort       *SortParams    `json:"sort,omitempty"`
}

// PaginationMeta contains pagination metadata.
type PaginationMeta struct {
	Page       int  `json:"page"`
	PageSize   int  `json:"page_size"`
	Total      int  `json:"total"`
	TotalPages int  `json:"total_pages"`
	HasNext    bool `json:"has_next"`
	HasPrev    bool `json:"has_prev"`
}

// NewPaginationMeta creates pagination metadata.
func NewPaginationMeta(params PaginationParams, total int) PaginationMeta {
	totalPages := (total + params.PageSize - 1) / params.PageSize
	if totalPages < 1 {
		totalPages = 1
	}

	return PaginationMeta{
		Page:       params.Page,
		PageSize:   params.PageSize,
		Total:      total,
		TotalPages: totalPages,
		HasNext:    params.Page < totalPages,
		HasPrev:    params.Page > 1,
	}
}
