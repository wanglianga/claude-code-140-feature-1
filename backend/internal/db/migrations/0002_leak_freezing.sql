-- 0002: 顶楼漏水投诉证据链、处置过程、收益冻结/按责任比例释放、屋顶巡检计划

-- 工单证据材料：施工照片 / 屋顶防水验收 / 雨量记录 / 顶楼住户同意签署 / 第三方检测报告
CREATE TABLE work_order_evidences (
    id           BIGSERIAL PRIMARY KEY,
    work_order_id BIGINT NOT NULL REFERENCES work_orders(id) ON DELETE CASCADE,
    building_id  BIGINT NOT NULL REFERENCES buildings(id) ON DELETE CASCADE,
    evidence_type TEXT NOT NULL
        CHECK (evidence_type IN ('construction_photo','waterproof_acceptance','rainfall','consent','inspection_report')),
    title        TEXT NOT NULL DEFAULT '',
    detail       TEXT NOT NULL DEFAULT '',  -- 文字描述 / 验收或签署结论
    recorded_on  DATE,                      -- 证据对应日期（拍摄日/验收日/降雨日/签署日）
    amount       NUMERIC(12,2),             -- 数值类证据（如降雨量 mm）
    source       TEXT NOT NULL DEFAULT '',  -- 来源：气象局/施工单位/住户/第三方机构
    file_url     TEXT NOT NULL DEFAULT '',  -- 附件地址（演示环境只存引用串）
    created_by   BIGINT REFERENCES users(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_wo_evidence ON work_order_evidences(work_order_id);

-- 工单处置过程：临时止水 / 住户安抚 / 第三方检测（责任无法确认时的过程留痕）
CREATE TABLE work_order_actions (
    id           BIGSERIAL PRIMARY KEY,
    work_order_id BIGINT NOT NULL REFERENCES work_orders(id) ON DELETE CASCADE,
    building_id  BIGINT NOT NULL REFERENCES buildings(id) ON DELETE CASCADE,
    action_type  TEXT NOT NULL
        CHECK (action_type IN ('temporary_seal','resident_reassurance','third_party_inspection','repair','other')),
    content      TEXT NOT NULL DEFAULT '',
    cost         NUMERIC(12,2) NOT NULL DEFAULT 0,
    operator_id  BIGINT REFERENCES users(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_wo_actions ON work_order_actions(work_order_id);

-- 收益冻结单：漏水维修期间冻结"相关组件"（默认涉事顶楼住户名下组件，可指定范围）的收益
CREATE TABLE revenue_freezes (
    id            BIGSERIAL PRIMARY KEY,
    building_id   BIGINT NOT NULL REFERENCES buildings(id) ON DELETE CASCADE,
    work_order_id BIGINT NOT NULL REFERENCES work_orders(id) ON DELETE CASCADE,
    period_month  TEXT NOT NULL,                -- 冻结起始结算月份
    title         TEXT NOT NULL DEFAULT '',
    scope         TEXT NOT NULL DEFAULT 'targeted' CHECK (scope IN ('targeted','all')),
    status        TEXT NOT NULL DEFAULT 'frozen'
        CHECK (status IN ('frozen','released')),
    -- 责任比例（%）：施工方(安装商)赔付 / 运维基金(维修基金)承担 / 楼栋公共收益支付 / 业主自担
    installer_pct NUMERIC(7,4) NOT NULL DEFAULT 0,
    fund_pct      NUMERIC(7,4) NOT NULL DEFAULT 0,
    public_pct    NUMERIC(7,4) NOT NULL DEFAULT 0,
    owner_pct     NUMERIC(7,4) NOT NULL DEFAULT 0,
    liability_note TEXT NOT NULL DEFAULT '',
    frozen_total  NUMERIC(12,2) NOT NULL DEFAULT 0,  -- 累计冻结挂账金额（月结定稿时累加）
    released_to_owner   NUMERIC(12,2) NOT NULL DEFAULT 0, -- 按非业主责任比例释放给住户
    owner_borne_total   NUMERIC(12,2) NOT NULL DEFAULT 0, -- 业主自担（冲减冻结、不发放）
    installer_compensation NUMERIC(12,2) NOT NULL DEFAULT 0, -- 施工方赔付入账
    fund_borne    NUMERIC(12,2) NOT NULL DEFAULT 0, -- 运维(维修)基金承担
    public_borne  NUMERIC(12,2) NOT NULL DEFAULT 0, -- 楼栋公共收益承担
    created_by    BIGINT REFERENCES users(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_at   TIMESTAMPTZ,
    UNIQUE (work_order_id)
);
CREATE INDEX idx_freezes_building ON revenue_freezes(building_id);

-- targeted 冻结时显式指定的"相关组件"收益归属人（渗漏点附近支架对应的认购住户）；
-- 为空则回退为工单报修人。可覆盖房屋转让后组件已过户给新业主的情形。
CREATE TABLE freeze_targets (
    freeze_id BIGINT NOT NULL REFERENCES revenue_freezes(id) ON DELETE CASCADE,
    user_id   BIGINT NOT NULL REFERENCES users(id),
    PRIMARY KEY (freeze_id, user_id)
);

-- 冻结挂账明细：月结定稿时，相关住户每笔被冻结的分成在此挂账；释放时回写
CREATE TABLE freeze_ledger (
    id               BIGSERIAL PRIMARY KEY,
    freeze_id        BIGINT NOT NULL REFERENCES revenue_freezes(id) ON DELETE CASCADE,
    building_id      BIGINT NOT NULL REFERENCES buildings(id) ON DELETE CASCADE,
    settlement_id    BIGINT NOT NULL REFERENCES settlement_periods(id),
    user_id          BIGINT NOT NULL REFERENCES users(id),
    period_month     TEXT NOT NULL,
    amount           NUMERIC(12,2) NOT NULL,       -- 被冻结的住户分成
    released_amount  NUMERIC(12,2) NOT NULL DEFAULT 0,
    owner_borne      NUMERIC(12,2) NOT NULL DEFAULT 0,
    status           TEXT NOT NULL DEFAULT 'held' CHECK (status IN ('held','released')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_at      TIMESTAMPTZ
);
CREATE INDEX idx_freeze_ledger_freeze ON freeze_ledger(freeze_id);
CREATE INDEX idx_freeze_ledger_user ON freeze_ledger(user_id);
-- 同一冻结单下，同一住户在同一结算月只挂账一次（防定稿重放/多冻结重叠重复冻结）
CREATE UNIQUE INDEX uq_freeze_ledger_period_user ON freeze_ledger(freeze_id, settlement_id, user_id);

-- 屋顶巡检计划：工单处理结论写入后续巡检
CREATE TABLE roof_inspections (
    id             BIGSERIAL PRIMARY KEY,
    building_id    BIGINT NOT NULL REFERENCES buildings(id) ON DELETE CASCADE,
    work_order_id  BIGINT REFERENCES work_orders(id) ON DELETE SET NULL,
    scheduled_date DATE NOT NULL,
    title          TEXT NOT NULL DEFAULT '',
    focus_area     TEXT NOT NULL DEFAULT '',  -- 重点部位：支架穿层点/防水卷材收口/天沟...
    conclusion     TEXT NOT NULL DEFAULT '',  -- 引用的工单处理结论
    status         TEXT NOT NULL DEFAULT 'scheduled'
        CHECK (status IN ('scheduled','done','cancelled')),
    result_note    TEXT NOT NULL DEFAULT '',
    created_by     BIGINT REFERENCES users(id),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    done_at        TIMESTAMPTZ
);
CREATE INDEX idx_inspections_building ON roof_inspections(building_id, scheduled_date);
