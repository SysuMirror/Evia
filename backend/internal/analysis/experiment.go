package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
)

const (
	experimentZ95      = 1.959963984540054
	experimentAlpha    = 0.05
	experimentSRMAlpha = 0.001
)

type experimentRow struct {
	time                int64
	sequence            int64
	session, tab, id    string
	source              string
	isExposure          bool
	variantPresent      bool
	variant             string
	isOutcome           bool
	outcomeValuePresent bool
	outcomeValue        float64
}

func earlierExperimentEvent(previous, next experimentRow) bool {
	if previous.time != next.time {
		return previous.time < next.time
	}
	return next.session != "" &&
		next.session == previous.session &&
		next.tab == previous.tab &&
		next.source == previous.source &&
		next.sequence > previous.sequence &&
		previous.sequence > 0
}

type variantAccumulator struct {
	variant      string
	exposed      int
	contaminated int
	converted    int
	mean         float64
	m2           float64
}

func (v *variantAccumulator) addSample(x float64) {
	v.exposed++
	delta := x - v.mean
	v.mean += delta / float64(v.exposed)
	delta2 := x - v.mean
	v.m2 += delta * delta2
}

func (v *variantAccumulator) variance() float64 {
	if v.exposed < 2 {
		return 0
	}
	return v.m2 / float64(v.exposed-1)
}

func (v *variantAccumulator) stddev() float64 {
	variance := v.variance()
	if variance <= 0 {
		return 0
	}
	return math.Sqrt(variance)
}

// EvaluateExperiment assigns each exposed subject to the variant of its
// earliest valid exposure event (intent-to-treat), counts subjects exposed to
// multiple variants as contaminated, aggregates in-window post-exposure
// outcomes, and runs SRM plus two-sample significance tests against control.
func EvaluateExperiment(request Request, rows []map[string]any) ([]map[string]any, error) {
	return evaluateExperiment(context.Background(), request, rows)
}

func evaluateExperiment(ctx context.Context, request Request, rows []map[string]any) ([]map[string]any, error) {
	if request.Experiment == nil {
		return nil, Invalid("INVALID_EXPERIMENT", "实验分析配置缺失")
	}
	cfg := request.Experiment
	bySubject := map[string][]experimentRow{}
	for _, row := range rows {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		subject := stringValue(row["subject_id"])
		if subject == "" {
			continue
		}
		eventTime, ok := integer(row["event_time_ns"])
		if !ok {
			return nil, Invalid("INVALID_EXPERIMENT_EVENT", "实验事件时间无效")
		}
		seq, _ := integer(row["sequence"])
		rec := experimentRow{
			time:           eventTime,
			sequence:       seq,
			session:        stringValue(row["session_id"]),
			tab:            stringValue(row["tab_id"]),
			source:         stringValue(row["source_id"]),
			id:             stringValue(row["event_id"]),
			isExposure:     flagOn(row["is_exposure"]),
			variantPresent: flagOn(row["variant_present"]),
			isOutcome:      flagOn(row["is_outcome"]),
		}
		if rec.isExposure && rec.variantPresent {
			rec.variant = scalarLabel(row["variant_value"])
		}
		if rec.isOutcome && cfg.MetricMode == ExperimentMetricModeNumericMean && flagOn(row["outcome_value_present"]) {
			if val, ok := floatValue(row["outcome_value"]); ok {
				rec.outcomeValuePresent = true
				rec.outcomeValue = val
			}
		}
		bySubject[subject] = append(bySubject[subject], rec)
	}

	windowNs := cfg.WindowSeconds * 1_000_000_000
	accByVariant := map[string]*variantAccumulator{}
	totalExposed := 0

	for _, events := range bySubject {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		sort.SliceStable(events, func(i, j int) bool {
			return earlierExperimentEvent(events[i], events[j])
		})
		firstIdx := -1
		assigned := ""
		for i, ev := range events {
			if ev.isExposure && ev.variant != "" {
				firstIdx = i
				assigned = ev.variant
				break
			}
		}
		if firstIdx < 0 {
			continue
		}
		firstEv := events[firstIdx]
		deadline := firstEv.time + windowNs
		contaminated := false
		converted := false
		numericSum := 0.0

		for i := firstIdx; i < len(events); i++ {
			ev := events[i]
			if ev.isExposure && ev.variant != "" && ev.variant != assigned {
				contaminated = true
			}
			if !ev.isOutcome {
				continue
			}
			if ev.time < firstEv.time || ev.time > deadline {
				continue
			}
			if ev.time == firstEv.time && i < firstIdx {
				continue
			}
			if cfg.MetricMode == ExperimentMetricModeConversion {
				converted = true
			} else if ev.outcomeValuePresent {
				converted = true
				numericSum += ev.outcomeValue
			}
		}

		acc := accByVariant[assigned]
		if acc == nil {
			acc = &variantAccumulator{variant: assigned}
			accByVariant[assigned] = acc
		}
		totalExposed++
		if contaminated {
			acc.contaminated++
		}
		if converted {
			acc.converted++
		}
		if cfg.MetricMode == ExperimentMetricModeConversion {
			if converted {
				acc.addSample(1.0)
			} else {
				acc.addSample(0.0)
			}
		} else {
			acc.addSample(numericSum)
		}
	}

	if len(accByVariant) == 0 {
		return []map[string]any{}, nil
	}

	variants := make([]*variantAccumulator, 0, len(accByVariant))
	for _, acc := range accByVariant {
		variants = append(variants, acc)
	}
	sort.Slice(variants, func(i, j int) bool {
		iControl := variants[i].variant == cfg.ControlVariant
		jControl := variants[j].variant == cfg.ControlVariant
		if iControl != jControl {
			return iControl
		}
		if variants[i].exposed != variants[j].exposed {
			return variants[i].exposed > variants[j].exposed
		}
		return variants[i].variant < variants[j].variant
	})

	srmPValue := computeSRMPValue(variants, totalExposed, cfg.ExpectedSplits)
	srmAlert := len(variants) >= 2 && totalExposed >= 20 && srmPValue < experimentSRMAlpha

	control := accByVariant[cfg.ControlVariant]
	out := make([]map[string]any, 0, len(variants))
	for _, acc := range variants {
		isControl := acc.variant == cfg.ControlVariant
		share := 0.0
		if totalExposed > 0 {
			share = float64(acc.exposed) / float64(totalExposed)
		}
		metricVal := acc.mean
		metricStd := acc.stddev()
		if cfg.MetricMode == ExperimentMetricModeConversion && acc.exposed > 0 {
			metricVal = float64(acc.converted) / float64(acc.exposed)
			metricStd = math.Sqrt(metricVal * (1 - metricVal))
		}

		liftAbs := 0.0
		liftRel := 0.0
		ciLow := 0.0
		ciHigh := 0.0
		pValue := 1.0
		significant := false

		if !isControl && control != nil && control.exposed >= 2 && acc.exposed >= 2 {
			if cfg.MetricMode == ExperimentMetricModeConversion {
				pC := float64(control.converted) / float64(control.exposed)
				pT := float64(acc.converted) / float64(acc.exposed)
				liftAbs = pT - pC
				if pC > 0 {
					liftRel = liftAbs / pC
				}
				seCI := math.Sqrt(pC*(1-pC)/float64(control.exposed) + pT*(1-pT)/float64(acc.exposed))
				ciLow = liftAbs - experimentZ95*seCI
				ciHigh = liftAbs + experimentZ95*seCI
				pPool := float64(control.converted+acc.converted) / float64(control.exposed+acc.exposed)
				sePool := math.Sqrt(pPool * (1 - pPool) * (1.0/float64(control.exposed) + 1.0/float64(acc.exposed)))
				if sePool > 0 {
					z := math.Abs(liftAbs) / sePool
					pValue = math.Erfc(z / math.Sqrt2)
				} else if liftAbs == 0 {
					pValue = 1.0
				}
			} else {
				liftAbs = acc.mean - control.mean
				if control.mean != 0 {
					liftRel = liftAbs / math.Abs(control.mean)
				}
				varC := control.variance() / float64(control.exposed)
				varT := acc.variance() / float64(acc.exposed)
				se := math.Sqrt(varC + varT)
				if se > 0 {
					tStat := math.Abs(liftAbs) / se
					df := welchDF(varC, control.exposed, varT, acc.exposed)
					pValue = studentTTwoTailedPValue(tStat, df)
					tCrit := studentTCritical95(df)
					ciLow = liftAbs - tCrit*se
					ciHigh = liftAbs + tCrit*se
				} else if liftAbs == 0 {
					pValue = 1.0
				}
			}
			significant = pValue < experimentAlpha
		}

		out = append(out, map[string]any{
			"variant":               acc.variant,
			"is_control":            isControl,
			"exposed_subjects":      acc.exposed,
			"contaminated_subjects": acc.contaminated,
			"sample_share":          roundStat(share, 6),
			"converted_subjects":    acc.converted,
			"metric_value":          roundStat(metricVal, 6),
			"metric_stddev":         roundStat(metricStd, 6),
			"lift_absolute":         roundStat(liftAbs, 6),
			"lift_relative":         roundStat(liftRel, 6),
			"ci_lower":              roundStat(ciLow, 6),
			"ci_upper":              roundStat(ciHigh, 6),
			"p_value":               roundStat(pValue, 6),
			"significant":           significant,
			"srm_p_value":           roundStat(srmPValue, 6),
			"srm_alert":             srmAlert,
		})
	}
	return out, nil
}

func scalarLabel(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return fmt.Sprint(typed)
	}
}

func floatValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case int64:
		return float64(typed), true
	case int:
		return float64(typed), true
	case json.Number:
		f, err := typed.Float64()
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	default:
		return 0, false
	}
}

func roundStat(value float64, decimals int) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	pow := math.Pow(10, float64(decimals))
	return math.Round(value*pow) / pow
}

func computeSRMPValue(variants []*variantAccumulator, total int, expectedSplits map[string]int) float64 {
	k := len(variants)
	if k < 2 || total <= 0 {
		return 1.0
	}
	weights := make([]float64, k)
	sumWeights := 0.0
	useExpected := len(expectedSplits) > 0
	if useExpected {
		for i, v := range variants {
			w, ok := expectedSplits[v.variant]
			if !ok || w <= 0 {
				useExpected = false
				break
			}
			weights[i] = float64(w)
			sumWeights += float64(w)
		}
	}
	if !useExpected || sumWeights <= 0 {
		for i := range weights {
			weights[i] = 1.0
		}
		sumWeights = float64(k)
	}
	chi2 := 0.0
	for i, v := range variants {
		expected := float64(total) * (weights[i] / sumWeights)
		if expected <= 0 {
			continue
		}
		diff := float64(v.exposed) - expected
		chi2 += (diff * diff) / expected
	}
	return chiSquareUpperTail(chi2, float64(k-1))
}

// chiSquareUpperTail computes P(Chi^2_df >= x) = Q(df/2, x/2).
func chiSquareUpperTail(x, df float64) float64 {
	if x <= 0 || df <= 0 {
		return 1.0
	}
	return regularizedGammaQ(df/2.0, x/2.0)
}

func regularizedGammaQ(a, x float64) float64 {
	if x <= 0 {
		return 1.0
	}
	if x < a+1.0 {
		return clampProbability(1.0 - regularizedGammaPSeries(a, x))
	}
	return clampProbability(regularizedGammaQCF(a, x))
}

func regularizedGammaPSeries(a, x float64) float64 {
	lg, _ := math.Lgamma(a)
	ap := a
	term := 1.0 / a
	sum := term
	for n := 1; n <= 200; n++ {
		ap++
		term *= x / ap
		sum += term
		if math.Abs(term) < math.Abs(sum)*1e-12 {
			break
		}
	}
	return sum * math.Exp(-x+a*math.Log(x)-lg)
}

func regularizedGammaQCF(a, x float64) float64 {
	lg, _ := math.Lgamma(a)
	b := x + 1.0 - a
	c := 1.0 / 1e-30
	d := 1.0 / b
	h := d
	for i := 1; i <= 200; i++ {
		an := -float64(i) * (float64(i) - a)
		b += 2.0
		d = an*d + b
		if math.Abs(d) < 1e-30 {
			d = 1e-30
		}
		c = b + an/c
		if math.Abs(c) < 1e-30 {
			c = 1e-30
		}
		d = 1.0 / d
		delta := d * c
		h *= delta
		if math.Abs(delta-1.0) < 1e-12 {
			break
		}
	}
	return math.Exp(-x+a*math.Log(x)-lg) * h
}

func welchDF(varC float64, nC int, varT float64, nT int) float64 {
	num := (varC + varT) * (varC + varT)
	den := 0.0
	if nC > 1 {
		den += (varC * varC) / float64(nC-1)
	}
	if nT > 1 {
		den += (varT * varT) / float64(nT-1)
	}
	if den <= 0 {
		return float64(nC + nT - 2)
	}
	df := num / den
	if df < 1 {
		return 1
	}
	return df
}

func studentTTwoTailedPValue(tAbs, df float64) float64 {
	if tAbs <= 0 {
		return 1.0
	}
	if df <= 0 || math.IsInf(df, 0) {
		return clampProbability(math.Erfc(tAbs / math.Sqrt2))
	}
	x := df / (df + tAbs*tAbs)
	return clampProbability(regularizedIncompleteBeta(df/2.0, 0.5, x))
}

func studentTCritical95(df float64) float64 {
	if df >= 10000 {
		return experimentZ95
	}
	if df < 1 {
		df = 1
	}
	lo, hi := 1.0, 15.0
	for studentTTwoTailedPValue(hi, df) > experimentAlpha && hi < 1000 {
		hi *= 2
	}
	for i := 0; i < 48; i++ {
		mid := (lo + hi) / 2
		if studentTTwoTailedPValue(mid, df) > experimentAlpha {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

// regularizedIncompleteBeta computes I_x(a, b) via Lentz's continued fraction.
func regularizedIncompleteBeta(a, b, x float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	lgAB, _ := math.Lgamma(a + b)
	lgA, _ := math.Lgamma(a)
	lgB, _ := math.Lgamma(b)
	bt := math.Exp(lgAB - lgA - lgB + a*math.Log(x) + b*math.Log(1.0-x))
	if x < (a+1.0)/(a+b+2.0) {
		return clampProbability(bt * betaCF(a, b, x) / a)
	}
	return clampProbability(1.0 - bt*betaCF(b, a, 1.0-x)/b)
}

func betaCF(a, b, x float64) float64 {
	const fpMin = 1e-30
	qab := a + b
	qap := a + 1.0
	qam := a - 1.0
	c := 1.0
	d := 1.0 - qab*x/qap
	if math.Abs(d) < fpMin {
		d = fpMin
	}
	d = 1.0 / d
	h := d
	for m := 1; m <= 200; m++ {
		mf := float64(m)
		m2 := 2.0 * mf
		aa := mf * (b - mf) * x / ((qam + m2) * (a + m2))
		d = 1.0 + aa*d
		if math.Abs(d) < fpMin {
			d = fpMin
		}
		c = 1.0 + aa/c
		if math.Abs(c) < fpMin {
			c = fpMin
		}
		d = 1.0 / d
		h *= d * c
		aa = -(a + mf) * (qab + mf) * x / ((a + m2) * (qap + m2))
		d = 1.0 + aa*d
		if math.Abs(d) < fpMin {
			d = fpMin
		}
		c = 1.0 + aa/c
		if math.Abs(c) < fpMin {
			c = fpMin
		}
		d = 1.0 / d
		delta := d * c
		h *= delta
		if math.Abs(delta-1.0) < 1e-12 {
			break
		}
	}
	return h
}

func clampProbability(p float64) float64 {
	if math.IsNaN(p) || p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}
