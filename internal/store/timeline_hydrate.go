package store

import (
	"context"
	"fmt"
	"slices"

	"go.kenn.io/docbank/document"
)

type TimelinePlacement struct {
	NodeID                   int64
	VersionID, EventID, Rule string
	AxisMinute               *int
	generationID             string
	tier                     int
	endDate                  string
}
type TimelinePerson struct {
	PersonID, DisplayName string
	Roles                 []string
	LinkedContact         bool
	ArchiveID, UID        string
}
type TimelineItem struct {
	TimelinePlacement
	Name, Path, MediaType, BlobHash string
	Size                            int64
	FamilyParentNodeID              *int64
	Event                           document.DocumentEventV1
	EndDate                         string
	People                          []TimelinePerson
	UnresolvedCount                 int
}

func timelineBindList(placements []TimelinePlacement) (string, []any) {
	args := make([]any, len(placements))
	for i, p := range placements {
		args[i] = p.VersionID
	}
	return numberedPlaceholders(len(placements), 0), args
}

// hydrateTimelineItems reads each group of versions in bounded statements;
// candidate evidence is fetched by bounded fresh-actor keys.
func (s *Store) hydrateTimelineItems(ctx context.Context, q metadataQuerier, placements []TimelinePlacement, hideLocked bool) ([]TimelineItem, error) {
	result := make([]TimelineItem, len(placements))
	byVersion := make(map[string]int, len(placements))
	events := make(map[string][]document.DocumentEventV1, len(placements))
	candidateEligible := make(map[string]bool, len(placements))
	for i, p := range placements {
		result[i] = TimelineItem{TimelinePlacement: p, EndDate: p.endDate, People: []TimelinePerson{}}
		byVersion[p.VersionID] = i
	}
	for start := 0; start < len(placements); start += 249 {
		chunk := placements[start:min(start+249, len(placements))]
		marks, args := timelineBindList(chunk)
		fingerprint := fmt.Sprintf("$%d", len(args)+1)
		args = append(args, document.PersonResolverFingerprint())
		hide := "0"
		if hideLocked {
			hide = "1"
		}
		rows, err := q.QueryContext(ctx, `WITH RECURSIVE dirs(dir_id,id,parent_id,path) AS (
   SELECT n.id,n.id,n.parent_id,n.name FROM nodes n WHERE n.id IN (SELECT parent_id FROM nodes WHERE current_version_id IN (`+marks+`))
   UNION ALL SELECT d.dir_id,n.id,n.parent_id,CASE WHEN n.name='' THEN '/'||d.path ELSE n.name||'/'||d.path END FROM nodes n JOIN dirs d ON n.id=d.parent_id
  ) SELECT cv.version_id,n.name,CASE WHEN dirs.path='/' THEN '/'||n.name ELSE dirs.path||'/'||n.name END,COALESCE(cv.mime_type,''),cv.blob_hash,cv.size,g.canonical_json,
  CASE WHEN h.content_version_id IS NULL THEN 1 WHEN `+currentPersonPublishedFence(false, hide, fingerprint)+` THEN 1 ELSE 0 END
  FROM content_versions cv JOIN nodes n ON n.id=cv.node_id JOIN dirs ON dirs.dir_id=n.parent_id AND dirs.parent_id IS NULL
  JOIN document_event_heads eh ON eh.content_version_id=cv.version_id JOIN document_event_generations g ON g.generation_id=eh.generation_id LEFT JOIN document_people_heads h ON h.content_version_id=cv.version_id CROSS JOIN document_people_state ps WHERE cv.version_id IN (`+marks+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var version, name, path, mime, hash string
			var size int64
			var candidateOK bool
			var raw []byte
			if err := rows.Scan(&version, &name, &path, &mime, &hash, &size, &raw, &candidateOK); err != nil {
				_ = rows.Close()
				return nil, err
			}
			record, _, err := document.DecodeDocumentEventsV1(raw)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			i := byVersion[version]
			item := &result[i]
			item.Name, item.Path, item.MediaType, item.BlobHash, item.Size = name, path, mime, hash, size
			events[version] = record.Events
			candidateEligible[version] = candidateOK
			found := false
			for _, event := range record.Events {
				if event.EventID == item.EventID {
					item.Event = event
					found = true
					break
				}
			}
			if !found {
				_ = rows.Close()
				return nil, fmt.Errorf("timeline event %s missing", item.EventID)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		// Shared fences keep assertions and derived edges in the same safe scope as queries.
		sqlText := `SELECT e.version_id,e.person_id,CASE WHEN e.derived=1 AND person.state='provisional' AND person.origin='derived' THEN e.raw_label ELSE person.display_name END,e.role,COALESCE(ext.archive_id,''),COALESCE(ext.uid,'') FROM (
   SELECT a.content_version_id AS version_id,COALESCE((SELECT surviving_person_id FROM person_aliases WHERE retired_person_id=a.person_id),a.person_id) AS person_id,a.role,'' AS raw_label,0 AS derived FROM person_document_assertions a WHERE a.content_version_id IN (` + marks + `) AND ` + currentPersonAssertionFence(true, hide) + `
   UNION SELECT p.content_version_id,COALESCE((SELECT surviving_person_id FROM person_aliases WHERE retired_person_id=p.person_id),p.person_id),p.role,p.raw_label,1 FROM document_people p JOIN document_people_heads h ON h.content_version_id=p.content_version_id AND h.generation_id=p.generation_id JOIN document_event_heads eh ON eh.content_version_id=h.content_version_id JOIN content_versions cv ON cv.version_id=p.content_version_id JOIN nodes n ON n.id=cv.node_id CROSS JOIN document_people_state ps WHERE p.content_version_id IN (` + marks + `) AND ` + currentPersonPublishedFence(true, hide, fingerprint) + `
  ) e JOIN persons person ON person.person_id=e.person_id LEFT JOIN person_external_identities ext ON ext.person_id=e.person_id AND ext.system='msgvault' AND ext.uid_state='current'
  ORDER BY e.version_id,e.person_id,e.derived,e.role,e.raw_label,ext.archive_id,ext.uid`
		rows, err = q.QueryContext(ctx, sqlText, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var version, id, name, role, archive, uid string
			if err := rows.Scan(&version, &id, &name, &role, &archive, &uid); err != nil {
				_ = rows.Close()
				return nil, err
			}
			item := &result[byVersion[version]]
			index := slices.IndexFunc(item.People, func(p TimelinePerson) bool { return p.PersonID == id })
			if index < 0 {
				item.People = append(item.People, TimelinePerson{PersonID: id, DisplayName: name, Roles: []string{}, ArchiveID: archive, UID: uid, LinkedContact: uid != ""})
				index = len(item.People) - 1
			}
			if !slices.Contains(item.People[index].Roles, role) {
				item.People[index].Roles = append(item.People[index].Roles, role)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	if len(placements) == 0 {
		return result, nil
	}
	var versions, actorKeys []string
	for version, recordEvents := range events {
		if !candidateEligible[version] {
			continue
		}
		versions = append(versions, version)
		for _, event := range recordEvents {
			if event.Sensitive {
				continue
			}
			for _, actor := range event.Actors {
				if !actor.Sensitive && actor.Role != "blind_copy" {
					actorKeys = append(actorKeys, actor.ActorKey)
				}
			}
		}
	}
	candidates, err := s.openCandidatesForVersions(ctx, q, versions, actorKeys)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		for _, o := range candidate.Occurrences {
			i, ok := byVersion[o.ContentVersionID]
			if !ok || !candidateEligible[o.ContentVersionID] || o.Role == "blind_copy" {
				continue
			}
			matched := false
			for _, event := range events[o.ContentVersionID] {
				if event.Sensitive {
					continue
				}
				for _, actor := range event.Actors {
					kind, id := actor.EvidenceKind, actor.EvidenceID
					if kind == "" {
						kind, id = event.EvidenceKind, event.EvidenceID
					}
					if !actor.Sensitive && actor.ActorKey == candidate.ActorKey && string(actor.Role) == o.Role && string(kind) == o.EvidenceKind && id == o.EvidenceID {
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}
			if matched {
				result[i].UnresolvedCount++
			}
		}
	}
	return result, nil
}
