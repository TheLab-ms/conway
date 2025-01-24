package auth

import "context"

type memberMetaKey struct{}

type UserMetadata struct {
	Email        string
	ActiveMember bool
	Leadership   bool
}

func withUserMeta(ctx context.Context, meta *UserMetadata) context.Context {
	return context.WithValue(ctx, memberMetaKey{}, meta)
}

// GetUserMeta returns the email address set by WithAuth from the request context.
func GetUserMeta(ctx context.Context) *UserMetadata {
	val := ctx.Value(memberMetaKey{})
	if val == nil {
		return nil
	}
	um, _ := val.(*UserMetadata)
	return um
}
