package discord

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

// CheckMessage reports the first way a message breaks Discord's component
// rules, so layouts fail in tests instead of as "interaction failed" in Discord.
// It covers the limits that matter for the views this app builds.
func CheckMessage(d ResponseData) error {
	c := &checker{ids: map[string]bool{}}
	if d.Flags&FlagComponentsV2 != 0 {
		if d.Content != "" || len(d.Embeds) > 0 {
			return errors.New("components v2 messages can't have content or embeds")
		}
		if len(d.Components) == 0 {
			return errors.New("components v2 message has no components")
		}
		for _, comp := range d.Components {
			if err := c.top(comp); err != nil {
				return err
			}
		}
		if c.count > 40 {
			return fmt.Errorf("%d components; the limit is 40", c.count)
		}
		if c.text > 4000 {
			return fmt.Errorf("%d characters of text; the limit is 4000", c.text)
		}
		return nil
	}
	if len(d.Components) > 5 {
		return fmt.Errorf("%d action rows; the limit is 5", len(d.Components))
	}
	for _, comp := range d.Components {
		if comp.Type != ComponentActionRow {
			return fmt.Errorf("component type %d needs the components v2 flag", comp.Type)
		}
		if err := c.row(comp); err != nil {
			return err
		}
	}
	return nil
}

type checker struct {
	count, text int
	ids         map[string]bool
}

func (c *checker) top(comp Component) error {
	switch comp.Type {
	case ComponentContainer:
		c.count++
		if len(comp.Components) == 0 {
			return errors.New("empty container")
		}
		for _, child := range comp.Components {
			if child.Type == ComponentContainer {
				return errors.New("containers can't be nested")
			}
			if err := c.top(child); err != nil {
				return err
			}
		}
		return nil
	case ComponentActionRow:
		return c.row(comp)
	case ComponentSection:
		return c.section(comp)
	case ComponentTextDisplay:
		return c.textDisplay(comp)
	case ComponentMediaGallery:
		c.count++
		if n := len(comp.Items); n < 1 || n > 10 {
			return fmt.Errorf("media gallery has %d items; it needs 1-10", n)
		}
		for _, it := range comp.Items {
			if it.Media.URL == "" {
				return errors.New("media gallery item without a url")
			}
		}
		return nil
	case ComponentSeparator:
		c.count++
		if comp.Spacing != 0 && comp.Spacing != SpacingSmall && comp.Spacing != SpacingLarge {
			return fmt.Errorf("separator spacing %d", comp.Spacing)
		}
		return nil
	}
	return fmt.Errorf("component type %d can't go here", comp.Type)
}

func (c *checker) section(comp Component) error {
	c.count++
	if n := len(comp.Components); n < 1 || n > 3 {
		return fmt.Errorf("section has %d text blocks; it needs 1-3", n)
	}
	for _, t := range comp.Components {
		if t.Type != ComponentTextDisplay {
			return errors.New("sections hold only text displays")
		}
		if err := c.textDisplay(t); err != nil {
			return err
		}
	}
	if comp.Accessory == nil {
		return errors.New("section without an accessory")
	}
	switch comp.Accessory.Type {
	case ComponentThumbnail:
		c.count++
		if comp.Accessory.Media == nil || comp.Accessory.Media.URL == "" {
			return errors.New("thumbnail without a url")
		}
		return nil
	case ComponentButton:
		return c.button(*comp.Accessory)
	}
	return fmt.Errorf("section accessory type %d", comp.Accessory.Type)
}

func (c *checker) textDisplay(comp Component) error {
	c.count++
	if comp.Content == "" {
		return errors.New("empty text display")
	}
	c.text += utf8.RuneCountInString(comp.Content)
	return nil
}

func (c *checker) row(comp Component) error {
	c.count++
	n := len(comp.Components)
	if n == 0 {
		return errors.New("empty action row")
	}
	if comp.Components[0].Type == ComponentStringSelect {
		if n != 1 {
			return errors.New("a select menu must be alone in its row")
		}
		return c.selectMenu(comp.Components[0])
	}
	if n > 5 {
		return fmt.Errorf("action row has %d buttons; the limit is 5", n)
	}
	for _, b := range comp.Components {
		if b.Type != ComponentButton {
			return fmt.Errorf("action row mixes component type %d with buttons", b.Type)
		}
		if err := c.button(b); err != nil {
			return err
		}
	}
	return nil
}

func (c *checker) button(b Component) error {
	c.count++
	if b.Label == "" || utf8.RuneCountInString(b.Label) > 80 {
		return fmt.Errorf("button label %q must be 1-80 characters", b.Label)
	}
	if b.Style == ButtonLink {
		if b.URL == "" || b.CustomID != "" {
			return fmt.Errorf("link button %q needs a url and no custom id", b.Label)
		}
		return nil
	}
	return c.customID(b.CustomID)
}

func (c *checker) selectMenu(s Component) error {
	c.count++
	if n := len(s.Options); n < 1 || n > 25 {
		return fmt.Errorf("select has %d options; it needs 1-25", n)
	}
	for _, o := range s.Options {
		if o.Label == "" || len(o.Label) > 100 || o.Value == "" || len(o.Value) > 100 || len(o.Description) > 100 {
			return fmt.Errorf("select option %q is out of bounds", o.Label)
		}
	}
	if s.MaxValues > len(s.Options) || (s.MinValues != nil && (*s.MinValues > len(s.Options) || (s.MaxValues > 0 && *s.MinValues > s.MaxValues))) {
		return fmt.Errorf("select allows %d values but has %d options", s.MaxValues, len(s.Options))
	}
	return c.customID(s.CustomID)
}

func (c *checker) customID(id string) error {
	if id == "" || len(id) > 100 {
		return fmt.Errorf("custom id %q must be 1-100 characters", id)
	}
	if c.ids[id] {
		return fmt.Errorf("custom id %q is used twice", id)
	}
	c.ids[id] = true
	return nil
}
