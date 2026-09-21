-- 开发用商户。无真实微信参数，不能完成 WX_NATIVE 渠道下单。
INSERT INTO t_mch_info (mch_no, mch_name, mch_short_name, type, state, created_by)
VALUES ('M1680000001', '适配器联调商户', 'adapter', 1, 1, 'seed');

INSERT INTO t_mch_app (app_id, app_name, mch_no, state, app_secret, created_by)
VALUES ('60cc09bce4b0f1c0b83761c9', '默认应用', 'M1680000001', 1, 'jeepay-dev-app-secret', 'seed');
