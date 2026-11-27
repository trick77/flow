package sandboxes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"github.com/trick77/flow/internal/sandbox"
)

// ErrNoAddress means every assignable address of the sandbox range is held.
var ErrNoAddress = errors.New("sandboxes: no free address in the sandbox range")

// Addresses hands out sandbox addresses from sandbox.Network.Range. An address is held from a sandbox's first
// bring-up until the sandbox is deleted, so its NIC, the guest's configuration and the address Flow dials never
// change; a released address goes to the next sandbox that needs one. Two sandboxes never hold the same address,
// and no sandbox holds one that is not sandbox.Network.Assignable.
type Addresses interface {
	// Assign returns the address the sandbox holds, taking the lowest free one when it holds none (or holds one
	// that is not assignable). busy are addresses instances carry that the assignment may not know (an instance
	// whose row is gone): never handed out.
	Assign(ctx context.Context, sandboxID string, busy []netip.Addr) (netip.Addr, error)
	// Release frees the sandbox's address; releasing one that holds none is not an error.
	Release(ctx context.Context, sandboxID string) error
}

// lowestFree is the lowest assignable address of the range that taken does not contain.
func lowestFree(n sandbox.Network, taken func(netip.Addr) bool) (netip.Addr, error) {
	for a := n.Range.Masked().Addr(); n.Range.Contains(a); a = a.Next() {
		if n.Assignable(a) && !taken(a) {
			return a, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("%w %s", ErrNoAddress, n.Range)
}

// MemoryAddresses keeps the assignment in memory. It is collision-free across a Flow restart only when seeded with
// what the instances carry (Manager does this from their NICs): Incus keeps each NIC's ipv4.address.
type MemoryAddresses struct {
	net  sandbox.Network
	mu   sync.Mutex
	held map[string]netip.Addr
}

var _ Addresses = (*MemoryAddresses)(nil)

// NewMemoryAddresses returns an assignment that starts with held (sandbox id or any other key → address);
// addresses that are not assignable are ignored, and of two keys holding the same address the lower one keeps it.
func NewMemoryAddresses(n sandbox.Network, held map[string]netip.Addr) *MemoryAddresses {
	m := &MemoryAddresses{net: n, held: map[string]netip.Addr{}}
	used := map[netip.Addr]bool{}
	for _, k := range slices.Sorted(maps.Keys(held)) {
		if a := held[k]; n.Assignable(a) && !used[a] {
			m.held[k], used[a] = a, true
		}
	}
	return m
}

// Assign implements Addresses.
func (m *MemoryAddresses) Assign(_ context.Context, sandboxID string, busy []netip.Addr) (netip.Addr, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.held[sandboxID]; ok {
		return a, nil
	}
	used := map[netip.Addr]bool{}
	for _, a := range m.held {
		used[a] = true
	}
	for _, a := range busy {
		used[a] = true
	}
	a, err := lowestFree(m.net, func(a netip.Addr) bool { return used[a] })
	if err != nil {
		return a, err
	}
	m.held[sandboxID] = a
	return a, nil
}

// Release implements Addresses.
func (m *MemoryAddresses) Release(_ context.Context, sandboxID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.held, sandboxID)
	return nil
}

// SQLAddresses keeps each sandbox's address in its row: column sandboxes.ipv4 (TEXT, NULL while none), under a
// unique index (CREATE UNIQUE INDEX sandboxes_ipv4 ON sandboxes (ipv4)), so two rows can never hold one address
// whatever races: SQLite lets any number of rows be NULL. Deleting a row frees its address. Not wired until the
// column exists.
type SQLAddresses struct {
	DB      *sql.DB
	Network sandbox.Network
}

var _ Addresses = SQLAddresses{}

// Assign implements Addresses: the row's address, or the lowest free one written into it. A concurrent Assign
// that took the same address first makes the unique index refuse the update; it is then tried again.
func (s SQLAddresses) Assign(ctx context.Context, sandboxID string, busy []netip.Addr) (netip.Addr, error) {
	for range 8 {
		a, retry, err := s.assign(ctx, sandboxID, busy)
		if !retry {
			return a, err
		}
	}
	return netip.Addr{}, fmt.Errorf("sandboxes: assigning an address to %s kept colliding", sandboxID)
}

func (s SQLAddresses) assign(ctx context.Context, sandboxID string, busy []netip.Addr) (netip.Addr, bool, error) {
	var own sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT ipv4 FROM sandboxes WHERE id = ?`, sandboxID).Scan(&own)
	if errors.Is(err, sql.ErrNoRows) {
		return netip.Addr{}, false, fmt.Errorf("%w: sandbox %s", ErrNotFound, sandboxID)
	}
	if err != nil {
		return netip.Addr{}, false, err
	}
	if own.Valid {
		// A stored address that is not assignable (written before the range changed, say) counts as none.
		if a, err := netip.ParseAddr(own.String); err == nil && s.Network.Assignable(a) {
			return a, false, nil
		}
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT ipv4 FROM sandboxes WHERE ipv4 IS NOT NULL`)
	if err != nil {
		return netip.Addr{}, false, err
	}
	used := map[netip.Addr]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			_ = rows.Close()
			return netip.Addr{}, false, err
		}
		if a, err := netip.ParseAddr(v); err == nil {
			used[a] = true
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return netip.Addr{}, false, err
	}
	for _, a := range busy {
		used[a] = true
	}
	a, err := lowestFree(s.Network, func(a netip.Addr) bool { return used[a] })
	if err != nil {
		return a, false, err
	}
	// Only over the value read above: a concurrent Assign that changed it wins, this one reads its result.
	res, err := s.DB.ExecContext(ctx, `UPDATE sandboxes SET ipv4 = ? WHERE id = ? AND ipv4 IS ?`, a.String(), sandboxID, own)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return netip.Addr{}, true, nil
		}
		return netip.Addr{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return netip.Addr{}, true, nil // a concurrent Assign for the same sandbox won: read its result
	}
	return a, false, nil
}

// Adopt records a for a sandbox whose row holds no address yet: the switch-over from MemoryAddresses, whose
// assignments live only on the instances' NICs (Manager adopts them before the first Assign). An address another
// row holds already stays with that row, and one that is not assignable is never adopted; this sandbox then gets a
// free one and its NIC follows on the next start.
func (s SQLAddresses) Adopt(ctx context.Context, sandboxID string, a netip.Addr) error {
	if !s.Network.Assignable(a) {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE sandboxes SET ipv4 = ? WHERE id = ? AND ipv4 IS NULL
		AND NOT EXISTS (SELECT 1 FROM sandboxes WHERE ipv4 = ?)`, a.String(), sandboxID, a.String())
	return err
}

// Release implements Addresses.
func (s SQLAddresses) Release(ctx context.Context, sandboxID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE sandboxes SET ipv4 = NULL WHERE id = ?`, sandboxID)
	return err
}
