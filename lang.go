package main

import "unicode"

// Characters whose traditional and simplified forms differ (common ones).
const tradChars = "們個這說來時為會國學對開關與樣經發從後過動見還進當點長門問間業實應現將義無體聽讀書話語論記東車馬鳥魚龍"
const simpChars = "们个这说来时为会国学对开关与样经发从后过动见还进当点长门问间业实应现将义无体听读书话语论记东车马鸟鱼龙"

// DetectLang guesses the main CJK script of a dictionary from a sample of
// headwords and definitions. It returns "" for non-CJK dictionaries.
func DetectLang(sd *StarDict) string {
	var han, kana, hangul, latin, trad, simp int
	isTrad := map[rune]bool{}
	isSimp := map[rune]bool{}
	for _, r := range tradChars {
		isTrad[r] = true
	}
	for _, r := range simpChars {
		isSimp[r] = true
	}
	count := func(s string) {
		for _, r := range s {
			switch {
			case unicode.Is(unicode.Han, r):
				han++
				if isTrad[r] {
					trad++
				} else if isSimp[r] {
					simp++
				}
			case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
				kana++
			case unicode.Is(unicode.Hangul, r):
				hangul++
			case r < 0x250 && unicode.IsLetter(r):
				latin++
			}
		}
	}
	n := len(sd.Entries)
	step := n / 1500
	if step < 1 {
		step = 1
	}
	for i := 0; i < n; i += step {
		count(sd.Entries[i].Word)
		if d, err := sd.RawData(i); err == nil {
			if len(d) > 600 {
				d = d[:600]
			}
			count(string(d))
		}
	}
	cjk := han + kana + hangul
	if cjk == 0 || cjk*5 < latin {
		// mostly non-CJK; still pick a CJK flavour if CJK is substantial
		if cjk*20 < latin {
			return ""
		}
	}
	switch {
	case kana*10 > han:
		return "ja"
	case hangul > han:
		return "ko"
	case trad > simp:
		return "zh-Hant"
	case simp > 0:
		return "zh-Hans"
	case han > 0:
		return "zh-Hant"
	}
	return ""
}
