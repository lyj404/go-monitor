//go:build linux

package collector

import (
	"encoding/binary"
	"fmt"
	"math/big"
	"net"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
)

type nftablesState struct {
	conn  *nftables.Conn
	table *nftables.Table
}

var nftState *nftablesState

var chainPolicyAccept = nftables.ChainPolicyAccept

func init() {
	initNftablesCounters = initNftablesCountersNetlink
	cleanupNftablesCounters = cleanupNftablesCountersNetlink
	readNftablesCounters = readNftablesCountersNetlink
}

func cidrToRangeV4(cidr string) (start, end []byte) {
	_, ipNet, _ := net.ParseCIDR(cidr)
	start = ipNet.IP.To4()
	prefixLen, _ := ipNet.Mask.Size()
	rangeSize := uint32(1) << (32 - prefixLen)
	startU32 := binary.BigEndian.Uint32(start)
	endU32 := startU32 + rangeSize
	end = make([]byte, 4)
	binary.BigEndian.PutUint32(end, endU32)
	return
}

func cidrToRangeV6(cidr string) (start, end []byte) {
	_, ipNet, _ := net.ParseCIDR(cidr)
	start = ipNet.IP.To16()
	prefixLen, _ := ipNet.Mask.Size()
	startBig := new(big.Int).SetBytes(start)
	rangeSize := new(big.Int).Lsh(big.NewInt(1), uint(128-prefixLen))
	endBig := new(big.Int).Add(startBig, rangeSize)
	end = make([]byte, 16)
	endBig.FillBytes(end)
	return
}

func initNftablesCountersNetlink() error {
	delConn, err := nftables.New()
	if err == nil {
		delConn.DelTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: "monitor"})
		_ = delConn.Flush()
	}

	conn, err := nftables.New()
	if err != nil {
		return fmt.Errorf("nftables conn: %w", err)
	}

	tbl := conn.AddTable(&nftables.Table{
		Family: nftables.TableFamilyINet,
		Name:   "monitor",
	})

	for _, name := range []string{"lan_ingress", "lan_egress", "wan_ingress", "wan_egress"} {
		conn.AddObj(&nftables.NamedObj{
			Table: tbl,
			Name:  name,
			Type:  nftables.ObjTypeCounter,
			Obj:   &expr.Counter{},
		})
	}

	prerouting := conn.AddChain(&nftables.Chain{
		Name:     "prerouting",
		Table:    tbl,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookPrerouting,
		Priority: nftables.ChainPriorityFilter,
		Policy:   &chainPolicyAccept,
	})

	// Create postrouting chain (egress)
	postrouting := conn.AddChain(&nftables.Chain{
		Name:     "postrouting",
		Table:    tbl,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookPostrouting,
		Priority: nftables.ChainPriorityFilter,
		Policy:   &chainPolicyAccept,
	})

	lanV4Set := &nftables.Set{
		Table:        tbl,
		Name:         "lan_v4",
		KeyType:      nftables.TypeIPAddr,
		Interval:     true,
		AutoMerge:    true,
		KeyByteOrder: binaryutil.BigEndian,
	}
	var v4Elems []nftables.SetElement
	for _, cidr := range lanV4CIDRs {
		start, end := cidrToRangeV4(cidr)
		v4Elems = append(v4Elems,
			nftables.SetElement{Key: start},
			nftables.SetElement{Key: end, IntervalEnd: true},
		)
	}
	if err := conn.AddSet(lanV4Set, v4Elems); err != nil {
		return fmt.Errorf("add lan_v4 set: %w", err)
	}

	lanV6Set := &nftables.Set{
		Table:        tbl,
		Name:         "lan_v6",
		KeyType:      nftables.TypeIP6Addr,
		Interval:     true,
		AutoMerge:    true,
		KeyByteOrder: binaryutil.BigEndian,
	}
	var v6Elems []nftables.SetElement
	for _, cidr := range lanV6CIDRs {
		start, end := cidrToRangeV6(cidr)
		v6Elems = append(v6Elems,
			nftables.SetElement{Key: start},
			nftables.SetElement{Key: end, IntervalEnd: true},
		)
	}
	if err := conn.AddSet(lanV6Set, v6Elems); err != nil {
		return fmt.Errorf("add lan_v6 set: %w", err)
	}

	// --- Prerouting rules (ingress) ---

	// IPv4 LAN ingress
	conn.AddRule(&nftables.Rule{
		Table: tbl,
		Chain: prerouting,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{0x02}},
			&expr.Payload{
				DestRegister: 1,
				Base:         expr.PayloadBaseNetworkHeader,
				Offset:       12,
				Len:          4,
			},
			&expr.Lookup{
				SourceRegister: 1,
				SetName:        lanV4Set.Name,
				SetID:          lanV4Set.ID,
			},
			&expr.Objref{Type: 1, Name: "lan_ingress"},
			&expr.Verdict{Kind: expr.VerdictReturn},
		},
	})

	// IPv4 WAN ingress (catch-all for IPv4)
	conn.AddRule(&nftables.Rule{
		Table: tbl,
		Chain: prerouting,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{0x02}},
			&expr.Objref{Type: 1, Name: "wan_ingress"},
		},
	})

	// IPv6 LAN ingress
	conn.AddRule(&nftables.Rule{
		Table: tbl,
		Chain: prerouting,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{0x0A}},
			&expr.Payload{
				DestRegister: 1,
				Base:         expr.PayloadBaseNetworkHeader,
				Offset:       8,
				Len:          16,
			},
			&expr.Lookup{
				SourceRegister: 1,
				SetName:        lanV6Set.Name,
				SetID:          lanV6Set.ID,
			},
			&expr.Objref{Type: 1, Name: "lan_ingress"},
			&expr.Verdict{Kind: expr.VerdictReturn},
		},
	})

	// IPv6 WAN ingress (catch-all for IPv6)
	conn.AddRule(&nftables.Rule{
		Table: tbl,
		Chain: prerouting,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{0x0A}},
			&expr.Objref{Type: 1, Name: "wan_ingress"},
		},
	})

	// --- Postrouting rules (egress) ---

	// IPv4 LAN egress
	conn.AddRule(&nftables.Rule{
		Table: tbl,
		Chain: postrouting,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{0x02}},
			&expr.Payload{
				DestRegister: 1,
				Base:         expr.PayloadBaseNetworkHeader,
				Offset:       16,
				Len:          4,
			},
			&expr.Lookup{
				SourceRegister: 1,
				SetName:        lanV4Set.Name,
				SetID:          lanV4Set.ID,
			},
			&expr.Objref{Type: 1, Name: "lan_egress"},
			&expr.Verdict{Kind: expr.VerdictReturn},
		},
	})

	// IPv4 WAN egress (catch-all for IPv4)
	conn.AddRule(&nftables.Rule{
		Table: tbl,
		Chain: postrouting,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{0x02}},
			&expr.Objref{Type: 1, Name: "wan_egress"},
		},
	})

	// IPv6 LAN egress
	conn.AddRule(&nftables.Rule{
		Table: tbl,
		Chain: postrouting,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{0x0A}},
			&expr.Payload{
				DestRegister: 1,
				Base:         expr.PayloadBaseNetworkHeader,
				Offset:       24,
				Len:          16,
			},
			&expr.Lookup{
				SourceRegister: 1,
				SetName:        lanV6Set.Name,
				SetID:          lanV6Set.ID,
			},
			&expr.Objref{Type: 1, Name: "lan_egress"},
			&expr.Verdict{Kind: expr.VerdictReturn},
		},
	})

	// IPv6 WAN egress (catch-all for IPv6)
	conn.AddRule(&nftables.Rule{
		Table: tbl,
		Chain: postrouting,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{0x0A}},
			&expr.Objref{Type: 1, Name: "wan_egress"},
		},
	})

	if err := conn.Flush(); err != nil {
		return fmt.Errorf("nftables flush: %w", err)
	}

	nftState = &nftablesState{conn: conn, table: tbl}
	return nil
}

func cleanupNftablesCountersNetlink() {
	if nftState == nil {
		return
	}
	nftState.conn.DelTable(nftState.table)
	_ = nftState.conn.Flush()
	nftState = nil
}

func readNftablesCountersNetlink() (lanIngress, lanEgress, wanIngress, wanEgress int64, err error) {
	if nftState == nil {
		return 0, 0, 0, 0, fmt.Errorf("nftables not initialized")
	}

	type namedCounter struct {
		name string
		ptr  *int64
	}
	counters := []namedCounter{
		{"lan_ingress", &lanIngress},
		{"lan_egress", &lanEgress},
		{"wan_ingress", &wanIngress},
		{"wan_egress", &wanEgress},
	}

	for _, c := range counters {
		obj, err := nftState.conn.GetObject(&nftables.NamedObj{
			Table: nftState.table,
			Name:  c.name,
			Type:  nftables.ObjTypeCounter,
			Obj:   &expr.Counter{},
		})
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("get counter %s: %w", c.name, err)
		}
		named, ok := obj.(*nftables.NamedObj)
		if !ok {
			return 0, 0, 0, 0, fmt.Errorf("unexpected type for %s", c.name)
		}
		cnt, ok := named.Obj.(*expr.Counter)
		if !ok {
			return 0, 0, 0, 0, fmt.Errorf("unexpected obj type for %s", c.name)
		}
		*c.ptr = int64(cnt.Bytes)
	}

	return
}
