package repository

import (
	"context"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"gorm.io/gorm"
)

type privateInvoiceReader struct{ db *gorm.DB }

func NewPrivateInvoiceReader(db *gorm.DB) domain.PrivateInvoiceReader {
	return &privateInvoiceReader{db}
}
func (r *privateInvoiceReader) ReadPrivateInvoice(ctx context.Context, s domain.PrivateInvoiceScope) (*domain.PrivateInvoice, error) {
	var result domain.PrivateInvoice
	err := r.db.WithContext(ctx).Model(&domain.Transaction{}).
		Select("id,enrollment_id,tenant_id,parent_id,student_id,status,checkout_session_url,invoice_expires_at").
		Where("id = ? AND enrollment_id = ? AND tenant_id = ? AND parent_id = ? AND student_id = ? AND private_schedule_request = true AND id = enrollment_id", s.TransactionID, s.EnrollmentID, s.TenantID, s.ParentID, s.StudentID).Take(&result).Error
	return &result, err
}
