#!/usr/bin/env bash
# words.sh DIR
#
# Counts the words on every NN-*.ansi capture in DIR with the DESIGN §9
# command (a word is a whitespace-separated token with a letter, on the
# ANSI-stripped capture; numbers are free) and checks it against the §9
# budget. Prints `scene  words  budget  PASS/FAIL` and exits 1 on any FAIL.
#
# Budgets are the §9 rows. Variants of a screen use their screen's row:
# 03b the full chart (03), 04 and 05 the inline chart (02), 07 the vault list
# (06), 13b the arena (13). 16b (plain text with no model) uses the §9 row
# added for it (16).
set -euo pipefail

DIR=${1:?usage: words.sh DIR}
[[ -d $DIR ]] || { echo "words: no such directory: $DIR" >&2; exit 2; }

budget() {
	case $1 in
	01) echo 16 ;;
	02 | 04 | 05) echo 16 ;;
	03 | 03b) echo 16 ;;
	06 | 07) echo 40 ;;
	06b) echo 120 ;;
	08) echo 30 ;;
	08b) echo 80 ;;
	09) echo 20 ;;
	10) echo 14 ;;
	11) echo 35 ;;
	12) echo 24 ;;
	13 | 13b) echo 30 ;;
	14) echo 25 ;;
	15) echo 80 ;;
	16) echo 12 ;;
	16b) echo 24 ;;
	17) echo 60 ;;
	18) echo 70 ;;
	*) echo - ;;
	esac
}

# words FILE ID: the §9 count, with two amendments (DESIGN §9):
#   - the top bar's ticker symbols count on 01 only (the ticker is the
#     landing's MarketFlap, chrome on every other screen): on row 1, a
#     symbol followed by a number is not a word;
#   - a number with a unit suffix ($151.7M, 3.5Y, 11X, 4D) is a number.
words() {
	perl -CSD -ne '
		BEGIN { $id = shift @ARGV; $n = 0 }
		s/\e\[[0-9;:]*[A-Za-z]//g; s/\e\][^\a]*\a//g;
		my @t = split /\s+/;
		for my $i (0 .. $#t) {
			my $w = $t[$i];
			next unless $w =~ /[A-Za-z]/;
			next if $w =~ /^[+\x{2212}\$-]?[0-9][0-9.,]*[KMBYX%DMO]*$/;
			next if $. == 1 && $id ne "01" && $w =~ /^[A-Z0-9]{2,8}$/ && $i < $#t && $t[$i+1] =~ /^[0-9]/;
			$n++;
		}
		END { print "$n\n" }
	' "$2" "$1"
}

shopt -s nullglob
files=("$DIR"/[0-9][0-9]*-*.ansi)
[[ ${#files[@]} -gt 0 ]] || { echo "words: no NN-*.ansi captures in $DIR" >&2; exit 2; }

fail=0
printf '%-6s %6s %7s  %s\n' scene words budget result
for f in "${files[@]}"; do
	base=$(basename "$f" .ansi)
	id=${base%%-*}
	n=$(words "$f" "$id")
	b=$(budget "$id")
	if [[ $b == - ]]; then
		r=-
	elif ((n <= b)); then
		r=PASS
	else
		r=FAIL
		fail=1
	fi
	printf '%-6s %6s %7s  %s\n' "$id" "$n" "$b" "$r"
done
exit $fail
