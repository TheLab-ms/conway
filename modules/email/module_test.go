package email

import (
	"context"
	"fmt"
	"testing"

	"github.com/TheLab-ms/conway/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMailDispatch(t *testing.T) {
	ctx := context.Background()
	db := db.NewTest(t)

	messages := []string{}
	m := New(db, func(ctx context.Context, to, subj string, msg []byte) error {
		messages = append(messages, fmt.Sprintf("to=%s subj=%s msg=%s", to, subj, msg))
		return nil
	})

	m.processNextMessage(ctx)
	assert.Equal(t, []string{}, messages)

	_, err := db.Exec("INSERT INTO outbound_mail (recipient, subject, body) VALUES ('foo@bar.com', 'Test!', 'hello world');")
	require.NoError(t, err)
	m.processNextMessage(ctx)
	assert.Equal(t, []string{"to=foo@bar.com subj=Test! msg=hello world"}, messages)

	m.processNextMessage(ctx)
	assert.Equal(t, []string{"to=foo@bar.com subj=Test! msg=hello world"}, messages)
}
