package service

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/repository"
)

// ErrItemNotFound means no media item has the requested id.
var ErrItemNotFound = errors.New("media item not found")

// MediaService answers the questions a browsing client asks: what libraries
// exist, what is in one, and what is this item.
//
// It knows nothing about Jellyfin. The compatibility layer translates its
// results, and so will the native API and the eventual administration UI.
type MediaService struct {
	pool *pgxpool.Pool
}

// NewMediaService returns a service backed by pool.
func NewMediaService(pool *pgxpool.Pool) *MediaService {
	return &MediaService{pool: pool}
}

// View is a library as something to browse, with the size of its contents.
type View struct {
	Library   domain.Library
	ItemCount int
}

// BrowseQuery describes a page of items.
//
// It is deliberately narrower than what a Jellyfin client can ask for: the
// compatibility layer is responsible for deciding what an unsupported request
// becomes, because that decision is about client behaviour rather than about
// media.
type BrowseQuery struct {
	LibraryIDs []uuid.UUID
	ItemIDs    []uuid.UUID
	MaxYear    *int

	// UserID is whose playback state travels with the items, and whose
	// progress the two filters below are judged against.
	UserID uuid.UUID

	// InProgressOnly answers "what am I part-way through".
	InProgressOnly bool

	// ExcludePlayed drops finished items.
	ExcludePlayed bool

	// PlayedOnly keeps only finished items.
	PlayedOnly bool

	Sort       repository.ItemSort
	Descending bool

	Offset int
	Limit  int
}

// BrowseResult is one page of items and the size of the whole result.
type BrowseResult struct {
	Items []repository.ItemWithFile
	Total int
}

// ItemDetail is a single item with everything needed to display and play it.
type ItemDetail struct {
	Item domain.MediaItem

	// State is the requesting user's progress through this item, zero when
	// they have never played it.
	State domain.PlaybackState

	// File and Streams describe the media itself. File is nil when the item
	// has no file row yet, which a scan interrupted between the two writes
	// can produce; callers must handle it rather than assume.
	File    *domain.MediaFile
	Streams []domain.MediaStream

	HasSubtitles bool
}

// maxBrowseLimit bounds a page.
//
// A client asking for everything gets a large but finite answer: an unbounded
// limit turns one request into an unbounded amount of work, and no client
// renders ten thousand rows at once anyway.
const maxBrowseLimit = 1000

// Views returns every library with the number of items in it.
//
// Two queries rather than one per library: the counts for every library are
// fetched in a single round trip.
func (s *MediaService) Views(ctx context.Context) ([]View, error) {
	libs := repository.NewLibraryRepository(s.pool)

	libraries, err := libs.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(libraries) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, len(libraries))
	for i, l := range libraries {
		ids[i] = l.ID
	}

	counts, err := repository.NewMediaRepository(s.pool).CountItemsByLibrary(ctx, ids)
	if err != nil {
		return nil, err
	}

	out := make([]View, len(libraries))
	for i, l := range libraries {
		out[i] = View{Library: l, ItemCount: counts[l.ID]}
	}
	return out, nil
}

// Browse returns a page of items and the total matching them.
func (s *MediaService) Browse(ctx context.Context, q BrowseQuery) (BrowseResult, error) {
	if q.Offset < 0 {
		return BrowseResult{}, InvalidArgumentf("offset must not be negative")
	}
	if q.Limit < 0 {
		return BrowseResult{}, InvalidArgumentf("limit must not be negative")
	}
	if q.Limit == 0 || q.Limit > maxBrowseLimit {
		q.Limit = maxBrowseLimit
	}

	sort := q.Sort
	if sort == "" {
		sort = repository.ItemSortTitle
	}

	items, total, err := repository.NewMediaRepository(s.pool).ListItems(ctx, repository.ItemQuery{
		LibraryIDs:     q.LibraryIDs,
		ItemIDs:        q.ItemIDs,
		MaxYear:        q.MaxYear,
		UserID:         q.UserID,
		InProgressOnly: q.InProgressOnly,
		ExcludePlayed:  q.ExcludePlayed,
		PlayedOnly:     q.PlayedOnly,
		Sort:           sort,
		Descending:     q.Descending,
		Offset:         q.Offset,
		Limit:          q.Limit,
	})
	if err != nil {
		return BrowseResult{}, err
	}
	return BrowseResult{Items: items, Total: total}, nil
}

// Item returns one media item with its file, streams, and the requesting
// user's progress through it, or ErrItemNotFound.
//
// A zero userID is allowed and yields zeroed state, for a caller with no user
// in hand.
func (s *MediaService) Item(ctx context.Context, id uuid.UUID, userID uuid.UUID) (ItemDetail, error) {
	media := repository.NewMediaRepository(s.pool)

	item, err := media.GetItem(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ItemDetail{}, fmt.Errorf("%w: %s", ErrItemNotFound, id)
		}
		return ItemDetail{}, err
	}

	state, err := repository.NewPlaybackRepository(s.pool).Get(ctx, userID, id)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return ItemDetail{}, err
	}
	// Never played is the common case, not an error: the zero value already
	// says position zero, unplayed, never.
	state.UserID, state.MediaItemID = userID, id

	files, err := media.ListFilesByItem(ctx, item.ID)
	if err != nil {
		return ItemDetail{}, err
	}

	detail := ItemDetail{Item: item, State: state}
	if len(files) == 0 {
		return detail, nil
	}

	// One file per item in 0.0.1. Multi-file items exist in the domain model
	// and will need a source per file; until then the first is the item.
	detail.File = &files[0]

	streams, err := media.ListStreams(ctx, detail.File.ID)
	if err != nil {
		return ItemDetail{}, err
	}
	detail.Streams = streams

	for _, st := range streams {
		if st.Kind == domain.StreamKindSubtitle {
			detail.HasSubtitles = true
			break
		}
	}
	return detail, nil
}
