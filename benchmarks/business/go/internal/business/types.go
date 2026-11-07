package business

// Principal 表达已验证的租户与用户，业务查询不得从请求正文覆盖身份。
type Principal struct {
	TenantID int64
	UserID   int64
}

type CatalogQuery struct {
	CategoryID   int64
	MinimumPrice int64
	MaximumPrice int64
	CursorPrice  int64
	CursorID     int64
	Limit        int
}

type ProductCard struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
	Category   string `json:"category"`
	Stock      int64  `json:"stock"`
}

type Cursor struct {
	PriceCents int64 `json:"price_cents"`
	ID         int64 `json:"id"`
}

type CatalogResponse struct {
	Items      []ProductCard `json:"items"`
	NextCursor *Cursor       `json:"next_cursor"`
}

type Specification struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// DetailResponse 只包含前端商品详情需要的字段，不包含内部成本与租户数据。
type DetailResponse struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	PriceCents  int64           `json:"price_cents"`
	Stock       int64           `json:"stock"`
	Category    string          `json:"category"`
	Description string          `json:"description"`
	Specs       []Specification `json:"specs"`
}

type OrderItemInput struct {
	ProductID int64 `json:"product_id"`
	Quantity  int64 `json:"quantity"`
}

type OrderInput struct {
	Items []OrderItemInput `json:"items"`
}

type OrderItem struct {
	ProductID      int64 `json:"product_id"`
	Quantity       int64 `json:"quantity"`
	UnitPriceCents int64 `json:"unit_price_cents"`
}

type OrderResponse struct {
	OrderID    int64       `json:"order_id"`
	TotalCents int64       `json:"total_cents"`
	Status     string      `json:"status"`
	Items      []OrderItem `json:"items"`
}

type CommentInput struct {
	Body string `json:"body"`
}

type CommentResponse struct {
	CommentID int64  `json:"comment_id"`
	TicketID  int64  `json:"ticket_id"`
	Version   int64  `json:"version"`
	Status    string `json:"status"`
}
