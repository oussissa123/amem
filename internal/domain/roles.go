package domain

import "fmt"

type Role int

const (
	RoleNone Role = iota
	RoleReader
	RoleContributor
	RoleCurator
	RoleAdmin
)

var roleNames = map[Role]string{RoleReader: "reader", RoleContributor: "contributor", RoleCurator: "curator", RoleAdmin: "admin"}

func (r Role) String() string { return roleNames[r] }

func ParseRole(s string) (Role, error) {
	for r, n := range roleNames {
		if n == s {
			return r, nil
		}
	}
	return RoleNone, fmt.Errorf("unknown role %q", s)
}

// transitions encodes RG-05: minimum role per (from, to). Superseded is only
// reached automatically (RG-06).
var transitions = map[Status]map[Status]Role{
	Proposed:  {Confirmed: RoleCurator, Disputed: RoleContributor, Obsolete: RoleCurator, Rejected: RoleCurator},
	Confirmed: {Disputed: RoleContributor, Obsolete: RoleCurator},
	Disputed:  {Confirmed: RoleCurator, Obsolete: RoleCurator, Rejected: RoleCurator},
	Obsolete:  {Confirmed: RoleCurator},
}

// CheckTransition applies RG-04 and RG-05.
func CheckTransition(from, to Status, role Role, isCreator, agentSelfConfirm bool, principalIsAgent bool, reason string) error {
	min, ok := transitions[from][to]
	if !ok {
		return Errf(CodeInvalidTransition, fmt.Sprintf("transition %s → %s is not allowed", from, to))
	}
	if role < min {
		return Errf(CodeForbidden, fmt.Sprintf("transition %s → %s requires role %s", from, to, min))
	}
	if to == Confirmed && isCreator && principalIsAgent && !agentSelfConfirm {
		return Errf(CodeForbidden, "an agent cannot confirm knowledge it created (RG-04)")
	}
	if len([]rune(reason)) < 3 || (to == Disputed && len([]rune(reason)) < 20) {
		return Errf(CodeValidation, "a reason is required (at least 20 characters to dispute)")
	}
	return nil
}
