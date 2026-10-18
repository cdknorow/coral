package store

import "context"

// DefaultTagColor is the colour of a tag created without one.
const DefaultTagColor = "#58a6ff"

// ResetInvalidTagColors gives the default colour to every tag whose stored colour is not #rgb, #rrggbb or #rrggbbaa (any case), and says how many
// it changed. It replaces a colour value and nothing else: no tag, no session or folder tag is deleted or renamed. GLOB matches the whole string, so
// each pattern is an exact length: '#' and then 3, 6 or 8 hexadecimal digits.
func (d *DB) ResetInvalidTagColors(ctx context.Context) (int64, error) {
	const hex = "[0-9a-fA-F]"
	h3, h6 := hex+hex+hex, hex+hex+hex+hex+hex+hex
	res, err := d.ExecContext(ctx, `UPDATE tags SET color = ? WHERE color NOT GLOB ? AND color NOT GLOB ? AND color NOT GLOB ?`,
		DefaultTagColor, "#"+h3, "#"+h6, "#"+h6+hex+hex)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
