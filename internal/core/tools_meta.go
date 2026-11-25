package core

import (
	"context"
	"reflect"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hlpxyz"
	"github.com/maxmoneycash/hlp/internal/paperx"
	"github.com/maxmoneycash/hlp/internal/pvault"
	"github.com/maxmoneycash/hlp/internal/registry"
)

// ToolSchema describes one tool for agents (`hlp schema <tool>`).
type ToolSchema struct {
	Name        string             `json:"name"`
	Command     string             `json:"command"`
	Slash       string             `json:"slash"`
	Aliases     []string           `json:"aliases,omitempty"`
	Positional  []string           `json:"positional,omitempty"`
	Group       string             `json:"group"`
	Summary     string             `json:"summary"`
	Description string             `json:"description,omitempty"`
	Mutating    bool               `json:"mutating"`
	Streaming   bool               `json:"streaming"`
	Card        string             `json:"card,omitempty"`
	OutputType  string             `json:"outputType,omitempty"`
	Input       *jsonschema.Schema `json:"input,omitempty"`
	Output      *jsonschema.Schema `json:"output,omitempty"`
	// Contract is the output contract version (see Contract in compact.go).
	Contract string `json:"contract"`
}

// toolOutputs maps tool names to their card and Result.Data type.
var toolOutputs = map[string]struct {
	card Card
	typ  reflect.Type
}{
	"get_markets":               {CardMarkets, reflect.TypeFor[[]domain.Market]()},
	"get_candles":               {CardChart, reflect.TypeFor[ChartData]()},
	"get_orderbook":             {CardBook, reflect.TypeFor[domain.Book]()},
	"get_trades":                {CardTrades, reflect.TypeFor[[]domain.Trade]()},
	"get_tick_candles":          {CardTicks, reflect.TypeFor[TickChartData]()},
	"get_funding":               {CardFunding, nil}, // see toolOutputAlts
	"get_asset_id":              {CardKV, reflect.TypeFor[AssetIDData]()},
	"get_account":               {CardAccount, reflect.TypeFor[domain.Account]()},
	"get_fills":                 {CardFills, reflect.TypeFor[[]domain.Fill]()},
	"watch":                     {CardAccount, reflect.TypeFor[domain.Account]()},
	"list_vaults":               {CardVaults, reflect.TypeFor[[]domain.VaultSummary]()},
	"get_vault":                 {CardVault, reflect.TypeFor[domain.VaultDetail]()},
	"compare_vaults":            {CardCompare, reflect.TypeFor[CompareData]()},
	"hlp_positioning":           {CardAccount, reflect.TypeFor[domain.Account]()},
	"list_indexes":              {CardIndex, reflect.TypeFor[IndexData]()},
	"get_vault_history":         {CardKV, reflect.TypeFor[VaultHistoryData]()},
	"get_strategy_vault":        {CardPVault, reflect.TypeFor[pvault.Tearsheet]()},
	"get_strategy_vault_epochs": {CardPVaultEpochs, reflect.TypeFor[pvault.EpochTable]()},
	"get_strategy_vault_user":   {CardPVaultUser, reflect.TypeFor[pvault.UserView]()},
	"discover_vaults":           {CardDiscover, reflect.TypeFor[registry.Discovery]()},
	"get_strategy":              {CardStrategy, reflect.TypeFor[StrategyData]()},
	"run_backtest":              {CardBacktest, reflect.TypeFor[BacktestData]()},
	"pine_check":                {CardKV, reflect.TypeFor[PineCheckData]()},
	"run_pine":                  {CardBacktest, reflect.TypeFor[BacktestData]()},
	"paper":                     {CardPaper, reflect.TypeFor[PaperData]()},
	"arena":                     {CardArena, reflect.TypeFor[ArenaData]()},
	"list_llm_models":           {CardKV, reflect.TypeFor[ModelsData]()},
	"get_macro":                 {CardKV, reflect.TypeFor[MacroData]()},
	"place_order":               {CardOrder, reflect.TypeFor[OrderPreview]()},
	"cancel_order":              {CardOrder, reflect.TypeFor[OrderPreview]()},
	"close_position":            {CardOrder, reflect.TypeFor[OrderPreview]()},
	"vault_deposit":             {CardOrder, reflect.TypeFor[OrderPreview]()},
	"vault_withdraw":            {CardOrder, reflect.TypeFor[OrderPreview]()},
	"schema":                    {CardSchema, reflect.TypeFor[[]schemaDoc]()}, // []ToolSchema; schemas as plain objects
	"get_price":                 {CardPrice, reflect.TypeFor[[]PriceRow]()},
	"preview_order":             {CardOrder, reflect.TypeFor[OrderPreview]()},
	"status":                    {CardStatus, reflect.TypeFor[StatusData]()},
	"paper_order":               {CardPaperOrder, reflect.TypeFor[PaperOrderData]()},
	"paper_cancel":              {CardPaperOrder, reflect.TypeFor[PaperOrderData]()},
	"paper_reset":               {CardPaperOrder, reflect.TypeFor[PaperOrderData]()},
	"paper_account":             {CardPaperAccount, reflect.TypeFor[paperx.Summary]()},
}

// schemaDoc is ToolSchema with its embedded JSON Schemas typed as plain
// objects: the output schema of the schema tool (jsonschema.Schema is
// recursive and cannot describe itself).
type schemaDoc struct {
	Name        string         `json:"name"`
	Command     string         `json:"command"`
	Slash       string         `json:"slash"`
	Aliases     []string       `json:"aliases,omitempty"`
	Positional  []string       `json:"positional,omitempty"`
	Group       string         `json:"group"`
	Summary     string         `json:"summary"`
	Description string         `json:"description,omitempty"`
	Mutating    bool           `json:"mutating"`
	Streaming   bool           `json:"streaming"`
	Card        string         `json:"card,omitempty"`
	OutputType  string         `json:"outputType,omitempty"`
	Input       map[string]any `json:"input,omitempty"`
	Output      map[string]any `json:"output,omitempty"`
	Contract    string         `json:"contract"`
}

// toolOutputAlts are the payload types of tools whose data takes one of
// several shapes; their output schema is anyOf these types.
var toolOutputAlts = map[string][]reflect.Type{
	// the ranking, or one coin's funding history when coin is given
	"get_funding": {reflect.TypeFor[[]domain.Market](), reflect.TypeFor[[]domain.FundingPoint]()},
}

// Describe returns the schema description of t (input schema always,
// output schema when it can be generated).
func Describe(t Tool) ToolSchema {
	ts := ToolSchema{
		Name: t.Name, Command: t.Command, Slash: t.Slash, Aliases: t.Aliases, Positional: t.Positional,
		Group: t.Group, Summary: t.Summary, Description: t.Description, Mutating: t.Mutating, Streaming: t.Streaming,
		Contract: Contract,
	}
	ts.Input, _ = t.InputSchema()
	if o, ok := toolOutputs[t.Name]; ok {
		ts.Card = string(o.card)
		switch {
		case o.typ != nil:
			ts.OutputType = o.typ.String()
			ts.Output, _ = jsonschema.ForType(o.typ, &jsonschema.ForOptions{})
		case len(toolOutputAlts[t.Name]) > 0:
			alts := toolOutputAlts[t.Name]
			names := make([]string, 0, len(alts))
			out := &jsonschema.Schema{}
			for _, a := range alts {
				names = append(names, a.String())
				sc, err := jsonschema.ForType(a, &jsonschema.ForOptions{})
				if err != nil {
					out = nil
					break
				}
				out.AnyOf = append(out.AnyOf, sc)
			}
			ts.OutputType, ts.Output = strings.Join(names, " | "), out
		}
	}
	return ts
}

// IndexData is the payload of list_indexes: every index (no slug), or one
// index with its latest allocation resolved to vault names.
type IndexData struct {
	Indexes []hlpxyz.Index `json:"indexes,omitempty"`
	Index   *IndexView     `json:"index,omitempty"`
}

// AssetIDData is the typed payload of get_asset_id (AGENT-UX #11): numbers
// as numbers. The KV card renders it through Pairs.
type AssetIDData struct {
	Coin         string `json:"coin"`
	Display      string `json:"display"`
	Kind         string `json:"kind"`
	Dex          string `json:"dex"` // "" = the main dex
	AssetID      int    `json:"assetId"`
	SzDecimals   int    `json:"szDecimals"`
	MaxLeverage  int    `json:"maxLeverage,omitempty"`
	OnlyIsolated bool   `json:"onlyIsolated,omitempty"`
	Delisted     bool   `json:"delisted,omitempty"`
	Category     string `json:"category,omitempty"`
}

// AssetIDFromMarket builds the get_asset_id payload for a resolved market.
func AssetIDFromMarket(m domain.Market) *AssetIDData {
	return &AssetIDData{Coin: m.Coin, Display: m.Display, Kind: string(m.Kind), Dex: m.Dex, AssetID: m.AssetID,
		SzDecimals: m.SzDecimals, MaxLeverage: m.MaxLeverage, OnlyIsolated: m.OnlyIsolated, Delisted: m.Delisted, Category: m.Category}
}

// Pairs is the key/value view the KV card renders.
func (a AssetIDData) Pairs() [][2]string {
	dex := a.Dex
	if dex == "" {
		dex = "(main)"
	}
	rows := [][2]string{{"coin", a.Coin}, {"display", a.Display}, {"kind", a.Kind}, {"dex", dex},
		{"assetId", strconv.Itoa(a.AssetID)}, {"szDecimals", strconv.Itoa(a.SzDecimals)}}
	if a.MaxLeverage > 0 {
		rows = append(rows, [2]string{"maxLeverage", strconv.Itoa(a.MaxLeverage)})
	}
	if a.OnlyIsolated {
		rows = append(rows, [2]string{"onlyIsolated", "true"})
	}
	if a.Delisted {
		rows = append(rows, [2]string{"delisted", "true"})
	}
	if a.Category != "" {
		rows = append(rows, [2]string{"category", a.Category})
	}
	return rows
}

func runSchema(reg func() *Registry) func(context.Context, *Env, any) (*Result, error) {
	return func(_ context.Context, env *Env, in any) (*Result, error) {
		p := in.(*SchemaInput)
		r := reg()
		if p.Tool == "" {
			var out []ToolSchema
			for _, t := range r.All() {
				d := Describe(t)
				d.Input, d.Output = nil, nil // the list stays small; ask per tool for schemas
				out = append(out, d)
			}
			return &Result{Card: CardSchema, Title: "Tools", Data: out, Meta: env.meta("hlp")}, nil
		}
		t, ok := r.Lookup(strings.TrimPrefix(p.Tool, "/"))
		if !ok {
			e := Errorf(CodeUsage, "unknown tool %q", p.Tool)
			e.Hint = "`hlp schema` lists every tool"
			if s := r.Suggest(p.Tool, 3); len(s) > 0 {
				e.Hint = "did you mean: " + strings.Join(s, ", ") + "? (`hlp schema` lists every tool)"
			}
			return nil, e
		}
		return &Result{Card: CardSchema, Title: t.Name, Data: []ToolSchema{Describe(t)}, Meta: env.meta("hlp")}, nil
	}
}
