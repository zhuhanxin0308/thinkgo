package business

import (
	"context"
	"database/sql"
	"errors"
)

func (service *Service) AddComment(ctx context.Context, principal Principal, id string, input CommentInput) (response CommentResponse, result error) {
	ticketID, err := recordID(id)
	if err != nil {
		return response, err
	}
	body, err := ValidateComment(input, service.config.Dataset)
	if err != nil {
		return response, err
	}
	tx, err := service.database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return response, err
	}
	defer rollback(tx, &result)
	var ownerID int64
	err = tx.QueryRowContext(ctx, service.queries["lock_ticket"], principal.TenantID, ticketID).
		Scan(&ownerID, &response.Status, &response.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return response, notFound()
	}
	if err != nil {
		return response, err
	}
	if ownerID != principal.UserID {
		return response, forbidden()
	}
	if response.Status != "open" {
		return response, conflict("工单已经关闭")
	}
	response.TicketID = ticketID
	err = tx.QueryRowContext(ctx, service.queries["insert_comment"], ticketID, principal.UserID, body).Scan(&response.CommentID)
	if err != nil {
		return response, err
	}
	err = tx.QueryRowContext(ctx, service.queries["update_ticket"], principal.TenantID, ticketID).Scan(&response.Version)
	if err != nil {
		return response, err
	}
	if err := tx.Commit(); err != nil {
		return response, err
	}
	return response, nil
}
