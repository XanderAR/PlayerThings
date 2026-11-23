package store

import "context"

// Unplayed returns a user's games with no recorded playtime, most reviewed
// (roughly, most acclaimed) first.
func (s *Store) Unplayed(ctx context.Context, discordID, platform string) ([]Game, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+gameColumns+`
		FROM library l JOIN games g ON g.platform = l.platform AND g.game_id = l.game_id
		WHERE l.discord_id = ? AND l.platform = ? AND l.minutes_total = 0
		ORDER BY g.reviews DESC, g.name`, discordID, platform)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Game
	for rows.Next() {
		var g Game
		if err := scanGame(rows, &g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
