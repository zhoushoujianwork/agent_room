package models

import "time"

// Room is the persistent record for a chat room. Rooms can be created
// anonymously (OwnerLogin == nil) — that's the existing flow — or by a
// signed-in GitHub user, in which case OwnerLogin contains their login
// and they can manage gating / title / lifecycle.
type Room struct {
	ID         string    `json:"room_id"`
	OwnerLogin *string   `json:"owner"`
	Title      *string   `json:"title"`
	Gated      bool      `json:"gated"`
	Ended      bool      `json:"ended"`
	CreatedAt  time.Time `json:"created_at"`
}

// AccessRequestStatus enumerates the lifecycle of a join request.
type AccessRequestStatus string

const (
	AccessRequestStatusPending  AccessRequestStatus = "pending"
	AccessRequestStatusApproved AccessRequestStatus = "approved"
	AccessRequestStatusDenied   AccessRequestStatus = "denied"
)

// AccessRequestPersistence captures how long an approval is valid.
// "once" = only for the connection that issued the request. "persist"
// = the requester is a permanent member of the room.
type AccessRequestPersistence string

const (
	AccessRequestPersistenceOnce    AccessRequestPersistence = "once"
	AccessRequestPersistencePersist AccessRequestPersistence = "persist"
)

// AccessRequest is a single join request against a gated room.
type AccessRequest struct {
	ID              string                    `json:"id"`
	RoomID          string                    `json:"room_id"`
	RequesterLogin  *string                   `json:"requester_login"`
	RequesterAnonID string                    `json:"-"` // server-side de-dupe key for anonymous requesters
	RequesterLabel  string                    `json:"requester_label"`
	Via             string                    `json:"via"`
	Location        *string                   `json:"location"`
	Status          AccessRequestStatus       `json:"status"`
	Persistence     *AccessRequestPersistence `json:"persistence,omitempty"`
	CreatedAt       time.Time                 `json:"created_at"`
	ResolvedAt      *time.Time                `json:"resolved_at,omitempty"`
}
