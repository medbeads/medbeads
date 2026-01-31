# MedBeads Sample Data

This folder contains sample patient data for testing the MedBeads system, including security clearance features.

## Structure

```
sample_data/
├── objects/          # Pre-generated Bead objects (CAS format)
├── fhir/             # FHIR Bundle JSON files for ingestion
│   ├── patient_a_gynecology.json
│   ├── patient_b_cancer_suspicion.json
│   ├── patient_c_psychiatry.json
│   ├── patient_d_general.json
│   └── patient_e_complex.json
├── setup_clearance_rules.py  # Script to set up security clearance rules
└── README.md
```

## Security Clearance Test Patients

### Patient A: Tanaka Yuki (30s Female) - Gynecology
- **Scenario**: Gynecological examination
- **Clearance**: Hide diagnosis and pregnancy test from **family**
- **Use Case**: Patient privacy for sensitive reproductive health information

### Patient B: Yamamoto Taro (50s Male) - Cancer Suspicion
- **Scenario**: Prostate cancer screening with elevated PSA
- **Clearance**: Temporarily hide diagnosis from **patient and family** (expires in 2 weeks)
- **Use Case**: Doctor needs time to prepare proper counseling session before disclosure

### Patient C: Suzuki Kenji (40s Male) - Psychiatry
- **Scenario**: Depression and anxiety treatment
- **Clearance**: Hide all mental health records from **insurance and admin**
- **Use Case**: Patient concerned about employment/insurance discrimination

### Patient D: Sato Hanako (60s Female) - General Internal Medicine
- **Scenario**: Routine health checkup (hypertension, blood work, flu vaccine)
- **Clearance**: **None** (all records accessible)
- **Use Case**: Normal patient with no special restrictions

### Patient E: Nakamura Ryo (20s Male) - Complex/Emergency
- **Scenario**: ER visit with multiple sensitive findings
  - Alcohol intoxication
  - Boxer's fracture
  - Positive drug screen (THC)
- **Clearance**: Multiple restrictions
  - Alcohol/drug information: Hide from **family, insurance, admin**
  - Drug screen: Hide from **all except primary care**
  - Social work notes: **Primary care only**
- **Use Case**: Complex case with legal/employment implications

## Usage

### 1. Ingest FHIR Data

```bash
cd medbeads/scripts
python mass_ingest.py ../sample_data/fhir/patient_a_gynecology.json --limit 1
# Repeat for other patients...
```

### 2. Setup Clearance Rules

```bash
cd medbeads/sample_data
python setup_clearance_rules.py --api-url http://localhost:8080
```

### 3. Test with Different Roles

Use the ViewerRoleSelector in the UI or pass `X-Viewer-Roles` header in API requests:

```bash
# As family member - should not see Patient A's gynecology records
curl -H "X-Viewer-Roles: family" http://localhost:8080/beads/context?id=PATIENT_A_ID

# As primary care doctor - should see everything
curl -H "X-Viewer-Roles: primary_care" http://localhost:8080/beads/context?id=PATIENT_E_ID

# Emergency override - bypasses all restrictions
curl -H "X-Viewer-Roles: emergency" http://localhost:8080/beads/context?id=PATIENT_B_ID
```

## Roles Reference

| Role | Description |
|------|-------------|
| `patient` | The patient themselves |
| `family` | Family members |
| `primary_care` | Primary care physician |
| `specialist` | Consulting specialists |
| `nurse` | Nursing staff |
| `admin` | Hospital administration |
| `insurance` | Insurance companies |
| `researcher` | Research access |
| `emergency` | Emergency override (bypasses all) |
| `system` | System/AI processes (full access) |
