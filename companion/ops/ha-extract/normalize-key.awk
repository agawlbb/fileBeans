# Canonicalize only the recovery-code prefix and hexadecimal letter case.
# The worker still validates the full structure and checksum.
BEGIN { FS=":" }
tolower($1)=="fbk1" && NF==3 && NR==1 {
 sub(/\r$/, "", $3)
 print "FBK1:" tolower($2) ":" tolower($3)
 next
}
{ exit 1 }
