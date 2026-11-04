package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type User struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Users struct {
	pool *pgxpool.Pool
}

func NewUsers(pool *pgxpool.Pool) *Users {
	return &Users{pool: pool}
}

func (u *Users) Create(ctx context.Context, name, email string) (User, error) {
	const query = `
		INSERT INTO users (name, email)
		VALUES ($1, $2)
		RETURNING id, name, email, created_at, updated_at`

	return scanUser(u.pool.QueryRow(ctx, query, name, email))
}

// Update changes only the fields that are not empty. Pass "" to leave a
// column unchanged.
func (u *Users) Update(ctx context.Context, id int64, name, email string) (User, error) {
	const query = `
		UPDATE users
		SET name       = COALESCE(NULLIF($2, ''), name),
		    email      = COALESCE(NULLIF($3, ''), email),
		    updated_at = now()
		WHERE id = $1
		RETURNING id, name, email, created_at, updated_at`

	user, err := scanUser(u.pool.QueryRow(ctx, query, id, name, email))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, fmt.Errorf("user %d not found", id)
	}

	return user, err
}

func (u *Users) Delete(ctx context.Context, id int64) error {
	tag, err := u.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return err
	}

	if tag.RowsAffected() == 0 {
		return fmt.Errorf("user %d not found", id)
	}

	return nil
}

func scanUser(row pgx.Row) (User, error) {
	var user User

	err := row.Scan(&user.ID, &user.Name, &user.Email, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return User{}, err
	}

	return user, nil
}
