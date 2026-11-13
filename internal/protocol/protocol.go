package protocol

import "time"

type SessionResponse struct {
	Token        string       `json:"token"`
	ExpiresIn    int64        `json:"expires_in"`
	UploadPolicy UploadPolicy `json:"upload_policy"`
}

type UploadPolicy struct {
	DirectMax int64  `json:"direct_max"`
	Tiers     []Tier `json:"tiers"`
	MaxPart   int64  `json:"max_part"`
}

type Tier struct {
	Max  int64 `json:"max"`
	Part int64 `json:"part"`
}

type RegisterResponse struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type AdminLoginResponse struct {
	Token     string `json:"token"`
	ExpiresIn int64  `json:"expires_in"`
}

type AddUserResponse struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type AdminUserInfo struct {
	Username   string   `json:"username"`
	System     bool     `json:"system"`
	Status     string   `json:"status"`
	PolicyName string   `json:"policy_name"`
	Groups     []string `json:"groups"`
}

type ListUsersResponse struct {
	Users []AdminUserInfo `json:"users"`
}

type FileInfo struct {
	Path         string    `json:"path"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
}

type ListResponse struct {
	Files []FileInfo `json:"files"`
}

type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

type PartInfo struct {
	Number int    `json:"number"`
	Size   int64  `json:"size"`
	ETag   string `json:"etag"`
}

type PartsResponse struct {
	Parts []PartInfo `json:"parts"`
}

type CompleteRequest struct {
	Parts []PartInfo `json:"parts"`
}

type InitResponse struct {
	UploadID string `json:"upload_id"`
	PartSize int64  `json:"part_size"`
}
